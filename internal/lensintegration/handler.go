package lensintegration

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/model"
)

// issueReader is the read-only slice of internal/issue.Store the AI
// cost handler uses. Kept local so the handler doesn't pull in the
// whole issue package.
type issueReader interface {
	GetByID(ctx context.Context, id string) (*model.Issue, error)
	GetByIdentifier(ctx context.Context, identifier string) (*model.Issue, error)
	TopByAICost(ctx context.Context, workspaceID string, limit int) ([]model.Issue, error)
}

// Handler serves the Track-side AI cost endpoints. Combines a live
// Lens client (for summary + anomalies) with the Track DB (for
// per-issue cost rollups).
type Handler struct {
	lens   *Client
	issues issueReader
}

func NewHandler(lens *Client, issues issueReader) *Handler {
	return &Handler{lens: lens, issues: issues}
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
	wsID := chi.URLParam(r, "wsID")
	ctx := r.Context()

	if !h.lens.IsConfigured() {
		writeJSON(w, http.StatusOK, map[string]any{
			"lens_configured": false,
		})
		return
	}

	type response struct {
		LensConfigured bool             `json:"lens_configured"`
		LensHealthy    bool             `json:"lens_healthy"`
		Summary        *SpendSummary    `json:"summary,omitempty"`
		TopIssues      []map[string]any `json:"top_issues"`
		Anomalies      []map[string]any `json:"anomalies"`
	}
	out := response{LensConfigured: true, LensHealthy: h.lens.Healthy(ctx)}

	if summary, err := h.lens.GetSpendSummary(ctx, wsID, 30); err == nil {
		out.Summary = summary
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

// GetIssueAICosts returns the per-issue AI cost rollup. Includes a
// link to the Lens dashboard so users can drill into the request-level
// detail without leaving the issue page.
func (h *Handler) GetIssueAICosts(w http.ResponseWriter, r *http.Request) {
	issue, err := h.issues.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || issue == nil {
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
	if h.lens.IsConfigured() {
		out["lens_url"] = h.lens.lensURL + "/dashboard"
	}
	writeJSON(w, http.StatusOK, out)
}
