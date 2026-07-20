package workspace_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workspace"
)

// wsReq injects the server-authorized workspace + role directly (the owner gate reads the
// ctx role that wsAuthz resolves in production).
func wsReq(method, wsID, role, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/v1/workspaces/"+wsID, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/v1/workspaces/"+wsID, nil)
	}
	return r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", role))
}

func wsExists(t *testing.T, d *testutil.DB, id string) bool {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM workspaces WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	return n > 0
}

// DELETE /v1/workspaces/{wsID} is owner-only: a member is refused and the workspace
// survives; an owner deletes it.
func TestWorkspace_Delete_OwnerGated(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	wsMem := d.Workspace(t)
	rr := httptest.NewRecorder()
	h.Delete(rr, wsReq(http.MethodDelete, wsMem.ID, authz.RoleMember, ""))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member delete = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if !wsExists(t, d, wsMem.ID) {
		t.Fatal("a member's delete removed the workspace")
	}

	wsOwn := d.Workspace(t)
	rr = httptest.NewRecorder()
	h.Delete(rr, wsReq(http.MethodDelete, wsOwn.ID, authz.RoleOwner, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner delete = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if wsExists(t, d, wsOwn.ID) {
		t.Fatal("owner delete did not remove the workspace")
	}
}

// PATCH /v1/workspaces/{wsID} is owner-only.
func TestWorkspace_Update_OwnerGated(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))
	ws := d.Workspace(t)

	rr := httptest.NewRecorder()
	h.Update(rr, wsReq(http.MethodPatch, ws.ID, authz.RoleMember, `{"name":"Hijacked"}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member update = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.Update(rr, wsReq(http.MethodPatch, ws.ID, authz.RoleOwner, `{"name":"Renamed"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner update = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}
