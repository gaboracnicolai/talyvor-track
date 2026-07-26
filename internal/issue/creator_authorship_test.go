package issue_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/testutil"
)

// AUDIT FINDING (forged authorship): issue.Handler.Create stamped WorkspaceID from the
// authorized context and then passed the REST of the decoded body straight to the store.
// CreatorID rode in from JSON. The store requires it non-empty but never checks it names
// a member — the ref-integrity loop covers project_id / cycle_id / assignee_id /
// parent_id, not creator_id — and issues.creator_id has no FK.
//
// So a member of workspace A could file issues signed by a member of workspace B, or by
// a string that names no member at all. The identical class was closed for COMMENTS
// (SEC-5: "the author is ALWAYS the verified session member") and never swept onto issue
// creation, which is how it survived. For a tracker whose product claim is attribution,
// forgeable authorship is a product defect, not only a security one.
//
// These tests forge it through the FULL middleware chain, exactly as the audit did.

// issueCreatorOf reads the persisted creator_id — the only thing that matters. The
// response body is a convenience; the row is the record.
func issueCreatorOf(t *testing.T, d *testutil.DB, issueID string) string {
	t.Helper()
	var creator string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT creator_id FROM issues WHERE id=$1`, issueID).Scan(&creator); err != nil {
		t.Fatalf("read creator_id: %v", err)
	}
	return creator
}

func createdIssueID(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		ID        string `json:"id"`
		CreatorID string `json:"creator_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode create response (%d): %s", rr.Code, rr.Body.String())
	}
	return out.ID
}

// RED before the fix: 201, creator_id = the foreign member's id.
// GREEN after: 201, creator_id = the CALLER's resolved member id, body value ignored.
func TestIssueCreate_ForgedCreator_IsIgnored_ForeignMember(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	wsB := d.Workspace(t)
	sec5Member(t, d, wsA.ID, "mallory@attacker.test")
	sec5Member(t, d, wsB.ID, "vera@victim.test")
	malloryID := memberID(t, d, wsA.ID, "mallory@attacker.test")
	veraID := memberID(t, d, wsB.ID, "vera@victim.test")
	teamA := d.Team(t, wsA.ID)
	chain := sec5IdentityChain(d)

	// Mallory is a member of wsA only. She files an issue in her OWN workspace but
	// signs it as Vera — a member of a DIFFERENT workspace.
	body := `{"team_id":"` + teamA.ID + `","title":"Signed by someone else","creator_id":"` + veraID + `"}`
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, postJSONAs(wsA.ID, "/issues", "mallory@attacker.test", body))

	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201 (the request itself is legitimate; only the actor is forged): %s", rr.Code, rr.Body.String())
	}
	got := issueCreatorOf(t, d, createdIssueID(t, rr))
	if got == veraID {
		t.Fatalf("FORGED AUTHORSHIP: issue persisted with creator_id=%q (vera, a member of a DIFFERENT workspace) — "+
			"the actor must come from the authenticated membership, never the request body", got)
	}
	if got != malloryID {
		t.Fatalf("creator_id = %q, want %q (mallory's resolved member id)", got, malloryID)
	}
}

// The same hole with a creator that names no member anywhere — issues.creator_id has no
// FK, so the store accepted arbitrary strings as authors.
func TestIssueCreate_ForgedCreator_IsIgnored_FabricatedActor(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	sec5Member(t, d, ws.ID, "mallory@attacker.test")
	malloryID := memberID(t, d, ws.ID, "mallory@attacker.test")
	team := d.Team(t, ws.ID)
	chain := sec5IdentityChain(d)

	body := `{"team_id":"` + team.ID + `","title":"Ghost author","creator_id":"totally-made-up-actor"}`
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, postJSONAs(ws.ID, "/issues", "mallory@attacker.test", body))

	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if got := issueCreatorOf(t, d, createdIssueID(t, rr)); got != malloryID {
		t.Fatalf("creator_id = %q, want %q — a fabricated actor string must never be persisted as the author", got, malloryID)
	}
}

// Omitting creator_id entirely must now SUCCEED: the server knows who the caller is, so
// requiring the client to name itself was never meaningful. This pins that the fix does
// not merely validate the supplied value but replaces it.
func TestIssueCreate_OmittedCreator_ResolvesToCaller(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	sec5Member(t, d, ws.ID, "alice@corp.com")
	aliceID := memberID(t, d, ws.ID, "alice@corp.com")
	team := d.Team(t, ws.ID)
	chain := sec5IdentityChain(d)

	body := `{"team_id":"` + team.ID + `","title":"No creator supplied"}`
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, postJSONAs(ws.ID, "/issues", "alice@corp.com", body))

	if rr.Code != http.StatusCreated {
		t.Fatalf("create without creator_id = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if got := issueCreatorOf(t, d, createdIssueID(t, rr)); got != aliceID {
		t.Fatalf("creator_id = %q, want %q (resolved from the caller's membership)", got, aliceID)
	}
}
