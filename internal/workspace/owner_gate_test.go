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

func wsName(t *testing.T, d *testutil.DB, id string) string {
	t.Helper()
	var name string
	if err := d.Pool.QueryRow(context.Background(), `SELECT name FROM workspaces WHERE id=$1`, id).Scan(&name); err != nil {
		t.Fatalf("read workspace name: %v", err)
	}
	return name
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

// PATCH /v1/workspaces/{wsID} is owner-only: a member is refused AND the settings do not
// change; an owner's rename lands.
//
// The 403 alone is not the property, and asserting it alone is why this gate was the one
// owner-gated write in the repository that nothing could see go inert. writeErr does not
// stop the handler: a gate that writes the refusal and falls through returns 403 with the
// rename APPLIED. Measured on this file before the read-back existed — dropping the
// `return` from the gate left the whole repository green, semgrep lock included.
func TestWorkspace_Update_OwnerGated(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))
	ws := d.Workspace(t)

	rr := httptest.NewRecorder()
	h.Update(rr, wsReq(http.MethodPatch, ws.ID, authz.RoleMember, `{"name":"Hijacked"}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member update = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if got := wsName(t, d, ws.ID); got != ws.Name {
		t.Fatalf("a member's REFUSED update renamed the workspace: %q -> %q", ws.Name, got)
	}

	rr = httptest.NewRecorder()
	h.Update(rr, wsReq(http.MethodPatch, ws.ID, authz.RoleOwner, `{"name":"Renamed"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner update = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// Must-stay-green companion: without it the refusal assertion above would also pass
	// on a handler that never writes at all, and would be justified by no mutation.
	if got := wsName(t, d, ws.ID); got != "Renamed" {
		t.Fatalf("owner update = 200 but the stored name is %q, want %q", got, "Renamed")
	}
}
