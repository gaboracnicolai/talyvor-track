package lensintegration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/model"
)

// lens_link_test.go — the per-issue AI-cost response must never hand a customer a link it
// has no basis to believe in.
//
// THE DEFECT. GetIssueAICosts emitted `lens_url = <TRACK_LENS_URL>/dashboard` whenever
// Client.IsConfigured() — which only means "a Lens API URL was configured". /dashboard is a
// BROWSER route on Lens gated by LENS_DASHBOARD_ENABLED, default FALSE, and Lens's shipped
// docker-compose does not forward that variable at all (it appears only in a comment), so on
// the standard deploy the path is unreachable no matter what the operator sets. Track was
// deriving a route on another service from a variable that says nothing about it.
//
// THE FIX. The destination is CONFIGURED, never derived, and absent when unconfigured — the
// key is omitted entirely rather than emitted empty, so a client cannot render a dead anchor
// out of a falsy value.

func issueReq(t *testing.T, ws string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/workspaces/"+ws+"/issues/iss-1/ai-costs", nil)
	req = req.WithContext(authz.WithAuthorized(req.Context(), ws, "m"))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "iss-1")
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rr.Body.String())
	}
	return out
}

func ownIssue() stubIssues {
	return stubIssues{issue: &model.Issue{
		ID: "iss-1", Identifier: "A-1", WorkspaceID: "ws-A", AICostUSD: 1.5,
	}}
}

// RED (the shipped defect): a configured Lens API URL made Track emit <lensURL>/dashboard —
// a 404 on every deployment that does not explicitly serve that browser route.
func TestIssueAICosts_NeverDerivesADashboardPathFromTheAPIURL(t *testing.T) {
	h := NewHandler(New("https://lens.example.com", "k"), ownIssue())

	rr := httptest.NewRecorder()
	h.GetIssueAICosts(rr, issueReq(t, "ws-A"))

	if strings.Contains(rr.Body.String(), "/dashboard") {
		t.Errorf("response derives a /dashboard path from the Lens API URL — that route is "+
			"gated off by default and unreachable through Lens's shipped compose: %s", rr.Body.String())
	}
	if _, present := decodeBody(t, rr)["lens_url"]; present {
		t.Errorf("lens_url emitted with no dashboard URL configured — a link to nothing: %s", rr.Body.String())
	}
}

// An unconfigured destination must OMIT the key, not emit an empty string. `"lens_url": ""`
// is falsy in JS and truthy as "the field exists" — the two readings disagree, and a client
// that checks presence renders a dead anchor.
func TestIssueAICosts_UnconfiguredDashboard_OmitsTheKeyEntirely(t *testing.T) {
	h := NewHandler(New("https://lens.example.com", "k"), ownIssue())

	rr := httptest.NewRecorder()
	h.GetIssueAICosts(rr, issueReq(t, "ws-A"))

	body := decodeBody(t, rr)
	if v, present := body["lens_url"]; present {
		t.Errorf("lens_url present as %#v — an unconfigured destination must omit the key", v)
	}
	// The cost payload itself is unaffected by the link's absence.
	if _, ok := body["ai_cost_usd"]; !ok {
		t.Errorf("ai_cost_usd missing — the link fix must not change the cost payload: %s", rr.Body.String())
	}
}

// When the operator DOES configure a dashboard URL, it is emitted verbatim — not
// concatenated with a guessed path.
func TestIssueAICosts_ConfiguredDashboard_EmittedVerbatim(t *testing.T) {
	const dash = "https://app.talyvor.com/lens/spend"
	h := NewHandler(New("https://lens.example.com", "k"), ownIssue()).WithDashboardURL(dash)

	rr := httptest.NewRecorder()
	h.GetIssueAICosts(rr, issueReq(t, "ws-A"))

	got, _ := decodeBody(t, rr)["lens_url"].(string)
	if got != dash {
		t.Errorf("lens_url = %q, want %q verbatim (no derived suffix)", got, dash)
	}
}

// A dashboard URL configured while Lens itself is NOT configured is still a valid link —
// the two are independent settings. This pins that the emit condition is the dashboard URL,
// not IsConfigured(): the old guard was the bug.
func TestIssueAICosts_DashboardURLIsIndependentOfTheLensAPIURL(t *testing.T) {
	const dash = "https://app.talyvor.com/lens/spend"
	h := NewHandler(New("", ""), ownIssue()).WithDashboardURL(dash)

	rr := httptest.NewRecorder()
	h.GetIssueAICosts(rr, issueReq(t, "ws-A"))

	got, _ := decodeBody(t, rr)["lens_url"].(string)
	if got != dash {
		t.Errorf("lens_url = %q, want %q — the link is gated on its OWN config, not on the Lens API URL", got, dash)
	}
}

// Whitespace-only configuration is not configuration.
func TestIssueAICosts_BlankDashboardURL_OmitsTheKey(t *testing.T) {
	h := NewHandler(New("https://lens.example.com", "k"), ownIssue()).WithDashboardURL("   ")

	rr := httptest.NewRecorder()
	h.GetIssueAICosts(rr, issueReq(t, "ws-A"))

	if v, present := decodeBody(t, rr)["lens_url"]; present {
		t.Errorf("lens_url present as %#v for a whitespace-only config", v)
	}
}
