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

func integrationRows(t *testing.T, d *testutil.DB, wsID, provider string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM workspace_integrations WHERE workspace_id=$1 AND provider=$2`,
		wsID, provider).Scan(&n); err != nil {
		t.Fatalf("count integrations: %v", err)
	}
	return n
}

// POST /v1/integrations writes a live provider credential — owner-only. A member (who
// passes membership) is refused AND no credential is stored; an owner succeeds.
//
// The 403 alone is not the property. writeErr does not stop the handler, so a gate that
// writes the refusal and falls through returns 403 with the CREDENTIAL WRITTEN — measured
// on this file before the read-back existed: dropping the `return` from the gate left the
// whole repository green. This is the write that matters most of the eight, because the
// thing that lands is a live provider token.
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
	if n := integrationRows(t, d, ws.ID, "linear"); n != 0 {
		t.Fatalf("a member's REFUSED set stored %d credential row(s), want 0", n)
	}

	// Owner succeeds.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, setReq(ws.ID, "owner@corp.com", `{"provider":"linear","token":"t"}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("owner set = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	// Must-stay-green companion: without it the refusal assertion above would also pass
	// on a handler that never stores anything, and would be justified by no mutation.
	if n := integrationRows(t, d, ws.ID, "linear"); n != 1 {
		t.Fatalf("owner set = 201 but %d credential row(s) stored, want 1", n)
	}
}
