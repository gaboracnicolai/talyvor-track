package lensintegration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/model"
)

// issueReader is the read-only slice of internal/issue.Store the AI
// cost handler uses. Kept local so the handler doesn't pull in the
// whole issue package.
type issueReader interface {
	GetByID(ctx context.Context, id string) (*model.Issue, error)
	GetByIdentifier(ctx context.Context, identifier, workspaceID string) (*model.Issue, error)
	TopByAICost(ctx context.Context, workspaceID string, limit int) ([]model.Issue, error)
}

// unattributedReader is the OPTIONAL seam for reading spend that reached no issue.
// *issue.Store satisfies it; a reader that does not simply omits the block rather than
// failing the request, so older wiring and narrow test doubles keep working.
type unattributedReader interface {
	UnattributedSpend(ctx context.Context, workspaceID string) (costUSD float64, requests int, err error)
}

// unattributedNote is the single sentence both endpoints carry beside the number. It says
// what the per-issue figures EXCLUDE, in the place where someone is reading them.
const unattributedNote = "AI spend that reached no issue: requests sent without an " +
	"X-Talyvor-Feature header, or with one matching no issue identifier. Per-issue costs " +
	"exclude it, so per-issue totals sum to less than the Lens bill by this amount."

// unattributedBlock reads the workspace's unattributed spend for the response. ok=false when
// the reader lacks the seam OR the read failed — in both cases the caller OMITS the block.
//
// A FAILED READ MUST NOT REPORT $0. Zero unattributed spend is a strong, checkable claim
// ("every dollar reached an issue"); a store that could not answer has not earned it, and a
// fabricated zero is indistinguishable from a real one at the point it is read.
// spendFailureReason turns a failed Lens read into something the operator can act on.
//
// ⚠ IT NAMES THE VARIABLE WHEN THE CREDENTIAL IS ABSENT, because that is the overwhelmingly common
// cause and the one a user cannot diagnose from a 401 they never see. Anything else is reported as
// what it is rather than guessed at.
func (h *Handler) spendFailureReason(err error) string {
	if h.lens.APIKey() == "" {
		return "Lens spend could not be read: TRACK_LENS_API_KEY is unset, so Track cannot " +
			"authenticate to Lens. Set it to a Lens workspace key (tlv_...). This is not the same " +
			"credential as TRACK_LENS_MINT_KEY, which enables the AI features."
	}
	return "Lens spend could not be read: " + err.Error()
}

func (h *Handler) unattributedBlock(ctx context.Context, wsID string) (map[string]any, bool) {
	reader, ok := h.issues.(unattributedReader)
	if !ok {
		return nil, false
	}
	cost, requests, err := reader.UnattributedSpend(ctx, wsID)
	if err != nil {
		slog.Warn("lensintegration: unattributed spend read failed — omitting rather than reporting $0",
			slog.String("workspace_id", wsID),
			slog.String("err", err.Error()),
		)
		return nil, false
	}
	return map[string]any{
		"cost_usd": cost,
		"requests": requests,
		"note":     unattributedNote,
	}, true
}

// Handler serves the Track-side AI cost endpoints. Combines a live
// Lens client (for summary + anomalies) with the Track DB (for
// per-issue cost rollups).
type Handler struct {
	lens   *Client
	issues issueReader

	// dashboardURL is the operator-declared human-facing cost UI, emitted as
	// `lens_url`. Empty (the default) means no link is emitted at all — see
	// WithDashboardURL.
	dashboardURL string
}

func NewHandler(lens *Client, issues issueReader) *Handler {
	return &Handler{lens: lens, issues: issues}
}

// WithDashboardURL sets the destination the AI-cost responses link to
// (TRACK_LENS_DASHBOARD_URL).
//
// IT IS CONFIGURED, NEVER DERIVED. Track previously built this link itself, as
// `<TRACK_LENS_URL>/dashboard`, guarded only by Client.IsConfigured() — which reports
// that a Lens *API* URL was set and nothing more. `/dashboard` is a BROWSER route on
// Lens, registered only when LENS_DASHBOARD_ENABLED is true (default false), and Lens's
// shipped docker-compose does not forward that variable at all — it appears there only
// inside a comment. So on the standard deploy the derived link 404s regardless of what
// the operator configures, and Track has no way to find that out: the flag lives in
// another service's process. A link that 404s is worse than no link, so Track stops
// guessing and asks.
//
// Blank or whitespace-only is treated as unset, and unset omits the key ENTIRELY rather
// than emitting "" — an empty string is falsy to one reader and "the field exists" to
// another, and a client that checks presence would render a dead anchor.
func (h *Handler) WithDashboardURL(u string) *Handler {
	h.dashboardURL = strings.TrimSpace(u)
	return h
}

// Mount registers the two AI cost routes plus the inbound webhook
// route's mounting helper. The webhook handler itself is mounted
// separately so callers can wire its dependencies (notification,
// realtime) independently.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/workspaces/{wsID}/ai-costs", h.GetAICosts)
	r.Get("/workspaces/{wsID}/issues/{id}/ai-costs", h.GetIssueAICosts)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// GetAICosts returns the workspace-level spend rollup. If Lens isn't
// configured the endpoint still works — it just returns
// `lens_configured: false` so the frontend can show a "set up Lens"
// CTA instead of failing.
func (h *Handler) GetAICosts(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "WORKSPACE_FORBIDDEN"})
		return
	}
	ctx := r.Context()

	// Unattributed spend is TRACK's own ledger, not a Lens read — it is reported whether or
	// not Lens is reachable or still configured, because the money was already recorded.
	unattributed, haveUnattributed := h.unattributedBlock(ctx, wsID)

	if !h.lens.IsConfigured() {
		body := map[string]any{"lens_configured": false}
		if haveUnattributed {
			body["unattributed"] = unattributed
		}
		writeJSON(w, http.StatusOK, body)
		return
	}

	type response struct {
		LensConfigured bool             `json:"lens_configured"`
		LensHealthy    bool             `json:"lens_healthy"`
		Summary        *SpendSummary    `json:"summary,omitempty"`
		TopIssues      []map[string]any `json:"top_issues"`
		Anomalies      []map[string]any `json:"anomalies"`
		// Unattributed is the spend that reached NO issue. It sits beside Summary
		// deliberately: Summary is the whole Lens bill and TopIssues is the attributed
		// subset, and without this field nothing on the response explains the gap.
		// Omitted (never zeroed) when it could not be read — see unattributedBlock.
		Unattributed map[string]any `json:"unattributed,omitempty"`
		// ⚠ THE DIFFERENCE BETWEEN "NOTHING WAS SPENT" AND "I COULD NOT ASK".
		//
		// Every Lens read below is best-effort, and each failure used to be swallowed by its own
		// `if err == nil`. With TRACK_LENS_URL set and no credential — the state this deployment
		// is actually in — every authenticated read 401s and the response came back as
		// {lens_configured: true, lens_healthy: true, top_issues: [], anomalies: []}: byte for
		// byte what a correctly configured workspace with genuinely zero spend returns. A total
		// that could not be obtained was being presented as a total that was measured.
		//
		// lens_healthy cannot cover this, because Lens serves /v1/api/health without a credential:
		// it is true precisely when the credential is missing. So the authenticated read reports
		// for itself. The same rule unattributedBlock already follows, one function above.
		SpendUnreadable bool   `json:"spend_unreadable,omitempty"`
		SpendReason     string `json:"spend_unreadable_reason,omitempty"`
	}
	out := response{LensConfigured: true, LensHealthy: h.lens.Healthy(ctx)}
	if haveUnattributed {
		out.Unattributed = unattributed
	}

	if summary, err := h.lens.GetSpendSummary(ctx, wsID, 30); err == nil {
		out.Summary = summary
	} else {
		out.SpendUnreadable = true
		out.SpendReason = h.spendFailureReason(err)
		slog.Warn("lensintegration: spend summary read failed — reporting it rather than showing $0",
			slog.String("workspace_id", wsID), slog.String("err", err.Error()))
	}
	if anoms, err := h.lens.GetAnomalies(ctx, wsID); err == nil {
		out.Anomalies = anoms
	}
	if out.Anomalies == nil {
		out.Anomalies = []map[string]any{}
	}

	// Top issues are pulled from Track's own DB — the ai_cost_usd
	// column carries the running total updated by the syncer and the
	// webhook. No Lens round-trip needed.
	if issues, err := h.issues.TopByAICost(ctx, wsID, 10); err == nil {
		for _, i := range issues {
			out.TopIssues = append(out.TopIssues, map[string]any{
				"issue_id":    i.ID,
				"identifier":  i.Identifier,
				"title":       i.Title,
				"ai_cost_usd": i.AICostUSD,
				"ai_tokens":   i.AITokens,
				"assignee_id": i.AssigneeID,
			})
		}
	}
	if out.TopIssues == nil {
		out.TopIssues = []map[string]any{}
	}

	writeJSON(w, http.StatusOK, out)
}

// GetIssueAICosts returns the per-issue AI cost rollup.
//
// It links onward ONLY when the operator has declared where to (WithDashboardURL /
// TRACK_LENS_DASHBOARD_URL). It used to build that link itself from the Lens API URL,
// which pointed every deployment at a route that is off by default — see
// WithDashboardURL for why Track cannot know whether that page exists.
func (h *Handler) GetIssueAICosts(w http.ResponseWriter, r *http.Request) {
	issue, err := h.issues.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || issue == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "issue not found"})
		return
	}
	// B-Track: the issue is fetched by bare id — verify it belongs to the caller's authorized workspace,
	// or a member of another workspace could read its AI cost/tokens by guessing the id. 404 (no oracle).
	if wsID, ok := authz.WorkspaceID(r.Context()); !ok || issue.WorkspaceID != wsID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "issue not found"})
		return
	}
	out := map[string]any{
		"issue_id":     issue.ID,
		"identifier":   issue.Identifier,
		"ai_cost_usd":  issue.AICostUSD,
		"ai_tokens":    issue.AITokens,
		"lens_feature": issue.LensFeature,
	}
	// ai_cost_usd above covers ONLY spend attributed to this issue. Name the workspace total
	// that reached no issue at all, so the figure is not read as the whole bill — which is
	// exactly how the frontend renders it (AICostBadge on rows, cards and issue detail).
	if un, ok := h.unattributedBlock(r.Context(), issue.WorkspaceID); ok {
		out["workspace_unattributed"] = un
	}
	if h.dashboardURL != "" {
		out["lens_url"] = h.dashboardURL
	}
	writeJSON(w, http.StatusOK, out)
}
