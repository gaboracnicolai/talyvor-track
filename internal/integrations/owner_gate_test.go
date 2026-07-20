package integrations_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/testutil"
)

func seedOwner(t *testing.T, d *testutil.DB, wsID, email string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'owner')`,
		wsID, email, email); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
}

// POST /v1/integrations writes a live provider credential — owner-only. A member (who
// passes membership) is refused; an owner succeeds.
func TestHandler_Set_OwnerGated(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "member@corp.com") // role member
	seedOwner(t, d, ws.ID, "owner@corp.com")   // role owner
	h := intChain(t, d)

	// Member is refused (403) even though they are a member of the workspace.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, setReq(ws.ID, "member@corp.com", `{"provider":"linear","token":"t"}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member set = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}

	// Owner succeeds.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, setReq(ws.ID, "owner@corp.com", `{"provider":"linear","token":"t"}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner set = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}
