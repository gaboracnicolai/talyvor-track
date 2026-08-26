package guest

// read_tenancy_test.go — THE TWO GUEST *READ* ROUTES HAD NO CROSS-WORKSPACE TEST, AND THEY ARE THE
// ONES THAT DISCLOSE DATA.
//
// Both open with the token-vs-URL guard:
//
//	wsID := chi.URLParam(r, "wsID")
//	if claims.WorkspaceID != wsID { writeErr(w, 403, "WS_MISMATCH", ...); return }
//
// and then read with the CALLER'S URL wsID, not with the token's claim. Delete the `return` and
// writeErr still writes the 403 header, execution CONTINUES, and the body of another workspace's
// data is appended after it. That is #173's inert-gate shape exactly: `writeErr` does not stop the
// handler.
//
// ⚠ MEASURED at bceb6c5, one `return` deleted at a time, whole repository, real Postgres + semgrep:
//
//	GuestCreateComment   semgrep NOT CAUGHT · go test CAUGHT (TestGuestComment_CrossWorkspace_403)
//	GuestListIssues      semgrep NOT CAUGHT · go test NOT CAUGHT
//	GuestGetIssue        semgrep NOT CAUGHT · go test NOT CAUGHT
//
// The WRITE route was covered and the two READS were covered by nothing — the same asymmetry #173
// found, where the sites that read the EFFECT back were exactly the sites that caught it.
//
// ⚠ WHY SEMGREP CANNOT SEE ANY OF THE THREE, WHICH IS THE HALF A TEST CANNOT FIX.
// .semgrep/workspace-authz.yml rule A forbids chi.URLParam(_, "wsID") and EXEMPTS a function
// containing `if $CLAIMS.WorkspaceID != $WS { ... }`. Semgrep's `...` matches ANY body, so a gate
// that writes the 403 and falls through satisfies the exemption and the rule stays green. #173
// found precisely this in owner-gate.yml and #174 strengthened THOSE rules; rule A was never
// touched. The rule now requires the comparison to `return`, so an inert guard cannot buy the
// exemption — and these tests hold the two instances.
//
// ⚠ THE ASSERTIONS READ THE BODY, NOT ONLY THE STATUS. A status-code assertion CANNOT tell an inert
// gate from an enforced one, because an inert gate still writes 403 — that is the whole reason the
// mutation above is invisible. What distinguishes them is whether the other workspace's rows come
// back in the payload, so that is what is asserted.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

// guestGet drives a mounted guest READ route with a bearer token.
func guestGet(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// seedProjectWithIssue creates a real project and stamps the issue into it, so the by-project read
// actually returns something. Without a populated project the list is empty for an innocent reason
// and every assertion below is vacuous. project_id carries a foreign key, so this cannot be faked
// with a literal — the FK refused one on the first run, which is the constraint doing its job.
func seedProjectWithIssue(t *testing.T, d *testutil.DB, wsID, teamID, issueID string) string {
	t.Helper()
	pr, err := project.NewStore(d.Pool).Create(context.Background(), model.Project{
		WorkspaceID: wsID, TeamID: teamID, Name: "guest read fixture", Identifier: "GRF",
	})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := d.Pool.Exec(context.Background(),
		`UPDATE issues SET project_id = $1 WHERE id = $2`, pr.ID, issueID); err != nil {
		t.Fatalf("stamp project_id: %v", err)
	}
	return pr.ID
}

// TestGuestListIssues_CrossWorkspace_403 — a guest whose token names workspace B asks for
// workspace A's issues by URL. The refusal must be ENFORCED, not merely written.
func TestGuestListIssues_CrossWorkspace_403(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	issA := d.Issue(t, wsA.ID, teamA.ID)
	projA := seedProjectWithIssue(t, d, wsA.ID, teamA.ID, issA.ID)
	h, gs := guestChain(d)
	path := "/guest/workspaces/" + wsA.ID + "/projects/" + projA + "/issues"

	// ── [R-PREMISE / MUST STAY GREEN] A's OWN guest can read A's issue. Without this the refusal
	// below would also pass on a route that returns nothing to anybody, and on a fixture whose
	// project stamp never landed — both of which make the tenancy assertion vacuous rather than
	// satisfied. It is the same must-stay-green companion #173 attached to every refusal.
	okTok := mintToken(gs, GuestClaims{GuestID: "g-a", WorkspaceID: wsA.ID, Role: GuestRoleViewer})
	rr := guestGet(h, path, okTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("[R-PREMISE] A's own guest got %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), issA.ID) {
		t.Fatalf("[R-PREMISE] A's own guest did not receive A's issue %s — the fixture returns an "+
			"empty list, so the cross-workspace assertion below would pass on nothing. body=%s",
			issA.ID, rr.Body.String())
	}

	// ── [R-LIST] The refusal itself, asserted on the PAYLOAD as well as the status.
	crossTok := mintToken(gs, GuestClaims{GuestID: "g-b", WorkspaceID: wsB.ID, Role: GuestRoleViewer})
	rr = guestGet(h, path, crossTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("[R-LIST] a workspace-B guest listing workspace A got %d, want 403; body=%s",
			rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), issA.ID) {
		t.Errorf("[R-LIST] a workspace-B guest RECEIVED workspace A's issue %s. The WS_MISMATCH "+
			"guard wrote its 403 and did not stop the handler — writeErr sets the header and "+
			"returns, it does not end the request — so the read below it ran with the CALLER'S "+
			"URL workspace. body=%s", issA.ID, rr.Body.String())
	}
}

// TestGuestGetIssue_CrossWorkspace_403 — the same guard on the by-id read.
func TestGuestGetIssue_CrossWorkspace_403(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	issA := d.Issue(t, wsA.ID, teamA.ID)
	h, gs := guestChain(d)
	path := "/guest/workspaces/" + wsA.ID + "/issues/" + issA.ID

	// ── [G-PREMISE / MUST STAY GREEN] A's own guest can read A's issue by id.
	okTok := mintToken(gs, GuestClaims{GuestID: "g-a", WorkspaceID: wsA.ID, Role: GuestRoleViewer})
	rr := guestGet(h, path, okTok)
	if rr.Code != http.StatusOK {
		t.Fatalf("[G-PREMISE] A's own guest got %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), issA.ID) {
		t.Fatalf("[G-PREMISE] A's own guest did not receive A's issue %s — the assertion below "+
			"would pass on an empty payload. body=%s", issA.ID, rr.Body.String())
	}

	// ── [G-GET] The refusal, asserted on the payload as well as the status.
	crossTok := mintToken(gs, GuestClaims{GuestID: "g-b", WorkspaceID: wsB.ID, Role: GuestRoleViewer})
	rr = guestGet(h, path, crossTok)
	if rr.Code != http.StatusForbidden {
		t.Errorf("[G-GET] a workspace-B guest reading workspace A's issue got %d, want 403; body=%s",
			rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), issA.ID) {
		t.Errorf("[G-GET] a workspace-B guest RECEIVED workspace A's issue %s — the WS_MISMATCH "+
			"guard wrote its 403 and fell through into the read. body=%s", issA.ID, rr.Body.String())
	}
}
