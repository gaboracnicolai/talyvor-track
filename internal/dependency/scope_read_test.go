package dependency_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/testutil"
)

func relListReq(wsID, issueID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+wsID+"/issues/"+issueID+"/relations/", nil)
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", authz.RoleMember))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", issueID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func seedRelation(t *testing.T, d *testutil.DB, src, tgt, wsID string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO issue_relations (source_id, target_id, type, workspace_id) VALUES ($1,$2,'blocks',$3)`,
		src, tgt, wsID); err != nil {
		t.Fatalf("seed relation: %v", err)
	}
}

// GET .../issues/{id}/relations must be workspace-scoped: a wsA member naming a wsB issue must
// not receive that issue's relations (the id is caller-supplied; the old query scoped the
// JOINED issue to the id's OWN workspace, never the caller's authorized workspace).
func TestDependency_ListRelations_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	tB := d.Team(t, wsB.ID)
	b1, b2 := d.Issue(t, wsB.ID, tB.ID), d.Issue(t, wsB.ID, tB.ID)
	seedRelation(t, d, b1.ID, b2.ID, wsB.ID)
	h := dependency.NewHandler(dependency.NewStore(d.Pool))

	rr := httptest.NewRecorder()
	h.List(rr, relListReq(wsA.ID, b1.ID))
	var got []map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got) != 0 {
		t.Fatalf("CROSS-WS LEAK: wsA caller saw %d relations of a wsB issue: %s", len(got), rr.Body.String())
	}

	// Positive: own-workspace issue's relations appear.
	tA := d.Team(t, wsA.ID)
	a1, a2 := d.Issue(t, wsA.ID, tA.ID), d.Issue(t, wsA.ID, tA.ID)
	seedRelation(t, d, a1.ID, a2.ID, wsA.ID)
	rrA := httptest.NewRecorder()
	h.List(rrA, relListReq(wsA.ID, a1.ID))
	var gotA []map[string]any
	_ = json.Unmarshal(rrA.Body.Bytes(), &gotA)
	if len(gotA) == 0 {
		t.Errorf("own-workspace relations should appear; got %s", rrA.Body.String())
	}
}
