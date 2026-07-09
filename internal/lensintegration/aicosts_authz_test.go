package lensintegration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/model"
)

type stubIssues struct{ issue *model.Issue }

func (s stubIssues) GetByID(context.Context, string) (*model.Issue, error) { return s.issue, nil }
func (s stubIssues) GetByIdentifier(context.Context, string) (*model.Issue, error) {
	return s.issue, nil
}
func (s stubIssues) TopByAICost(context.Context, string, int) ([]model.Issue, error) {
	return nil, nil
}

// B-Track: GetIssueAICosts fetched the issue by bare id (GetByID) and never compared issue.WorkspaceID
// to the caller's authorized workspace — so a member of workspace A read workspace B's issue AI cost by
// id. RED: the cost leaks. GREEN: a foreign issue is 404, the caller's own issue still returns.
func TestGetIssueAICosts_CrossTenant_Denied(t *testing.T) {
	h := NewHandler(New("", ""), stubIssues{issue: &model.Issue{
		ID: "iss-1", Identifier: "B-1", WorkspaceID: "ws-B", AICostUSD: 99.99,
	}})

	call := func(callerWS string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/workspaces/"+callerWS+"/issues/iss-1/ai-costs", nil)
		req = req.WithContext(authz.WithAuthorized(req.Context(), callerWS, "m"))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "iss-1")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		h.GetIssueAICosts(rr, req)
		return rr
	}

	// Attacker: authorized for ws-A, reads a ws-B issue by id.
	att := call("ws-A")
	if att.Code == http.StatusOK || strings.Contains(att.Body.String(), "99.99") {
		t.Errorf("cross-tenant LEAK: ws-A read ws-B issue AI cost (status %d): %s", att.Code, att.Body.String())
	}

	// POSITIVE: the issue's own workspace still reads it.
	ok := call("ws-B")
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), "99.99") {
		t.Errorf("owner (ws-B) should read its issue cost, got %d: %s", ok.Code, ok.Body.String())
	}
}
