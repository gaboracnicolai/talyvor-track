package project_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

func delProjReq(wsID, projID, role string) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, "/v1/workspaces/"+wsID+"/projects/"+projID, nil)
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", role))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func seedProject(t *testing.T, d *testutil.DB, wsID string) string {
	t.Helper()
	tm := d.Team(t, wsID)
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO projects (workspace_id, team_id, name, identifier) VALUES ($1,$2,$3,$4) RETURNING id`,
		wsID, tm.ID, "Proj "+tm.ID, "P"+tm.ID[:6]).Scan(&id); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return id
}

func projExists(t *testing.T, d *testutil.DB, id string) bool {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM projects WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	return n > 0
}

// DELETE /v1/workspaces/{wsID}/projects/{id} is owner-only.
func TestProject_Delete_OwnerGated(t *testing.T) {
	d := testutil.New(t)
	h := project.NewHandler(project.NewStore(d.Pool))
	ws := d.Workspace(t)

	pMem := seedProject(t, d, ws.ID)
	rr := httptest.NewRecorder()
	h.Delete(rr, delProjReq(ws.ID, pMem, authz.RoleMember))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member delete project = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !projExists(t, d, pMem) {
		t.Fatal("a member's delete removed the project")
	}

	pOwn := seedProject(t, d, ws.ID)
	rr = httptest.NewRecorder()
	h.Delete(rr, delProjReq(ws.ID, pOwn, authz.RoleOwner))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner delete project = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if projExists(t, d, pOwn) {
		t.Fatal("owner delete did not remove the project")
	}
}
