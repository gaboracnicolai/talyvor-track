package workflow_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workflow"
)

func listStatusesReq(wsID, teamID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+wsID+"/teams/"+teamID+"/statuses", nil)
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", authz.RoleMember))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("teamID", teamID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func seedStatus(t *testing.T, d *testutil.DB, teamID, name string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO workflow_statuses (team_id, name, color, category, position, is_default)
         VALUES ($1,$2,'#94a3b8','started',0,false)`, teamID, name); err != nil {
		t.Fatalf("seed status: %v", err)
	}
}

// GET .../teams/{teamID}/statuses must be scoped to the caller's authorized workspace: a
// wsA member naming a wsB team must see NONE of wsB's statuses (the teamID is caller-supplied).
func TestWorkflow_ListStatuses_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	seedStatus(t, d, teamB.ID, "B-Secret-Status")
	h := workflow.NewHandler(workflow.New(d.Pool))

	rr := httptest.NewRecorder()
	h.List(rr, listStatusesReq(wsA.ID, teamB.ID))
	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	for _, s := range got {
		if s["name"] == "B-Secret-Status" {
			t.Fatalf("CROSS-WS LEAK: wsA caller saw wsB team's status: %v", got)
		}
	}
	if len(got) != 0 {
		t.Errorf("wsA caller got %d status rows for a wsB team, want 0: %v", len(got), got)
	}

	// Positive: a team in the caller's own workspace still returns its statuses.
	teamA := d.Team(t, wsA.ID)
	seedStatus(t, d, teamA.ID, "A-Todo")
	rrA := httptest.NewRecorder()
	h.List(rrA, listStatusesReq(wsA.ID, teamA.ID))
	var gotA []map[string]any
	_ = json.Unmarshal(rrA.Body.Bytes(), &gotA)
	found := false
	for _, s := range gotA {
		if s["name"] == "A-Todo" {
			found = true
		}
	}
	if !found {
		t.Errorf("wsA caller should see its own team's status; got %v", gotA)
	}
}
