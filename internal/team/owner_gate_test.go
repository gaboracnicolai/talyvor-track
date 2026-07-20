package team_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/team"
	"github.com/talyvor/track/internal/testutil"
)

func delTeamReq(wsID, teamID, role string) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, "/v1/workspaces/"+wsID+"/teams/"+teamID, nil)
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", role))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", teamID)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func teamExists(t *testing.T, d *testutil.DB, id string) bool {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM teams WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatalf("count teams: %v", err)
	}
	return n > 0
}

// DELETE /v1/workspaces/{wsID}/teams/{id} is owner-only.
func TestTeam_Delete_OwnerGated(t *testing.T) {
	d := testutil.New(t)
	h := team.NewHandler(team.NewStore(d.Pool))
	ws := d.Workspace(t)

	tmMem := d.Team(t, ws.ID)
	rr := httptest.NewRecorder()
	h.Delete(rr, delTeamReq(ws.ID, tmMem.ID, authz.RoleMember))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member delete team = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !teamExists(t, d, tmMem.ID) {
		t.Fatal("a member's delete removed the team")
	}

	tmOwn := d.Team(t, ws.ID)
	rr = httptest.NewRecorder()
	h.Delete(rr, delTeamReq(ws.ID, tmOwn.ID, authz.RoleOwner))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner delete team = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if teamExists(t, d, tmOwn.ID) {
		t.Fatal("owner delete did not remove the team")
	}
}
