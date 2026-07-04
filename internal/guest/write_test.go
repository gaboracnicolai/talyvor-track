package guest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// ── Guest-write slice 1 — the role lock + tenancy + guest-actor for a guest commenter. These are NEW
//    assertions (the importer authz_test.go stays 0-diff — it has no role pattern). ──

const testGuestSecret = "test-guest-secret-0123456789abcdef"

func guestChain(d *testutil.DB) (http.Handler, *Store) {
	gs := NewStore(d.Pool, testGuestSecret)
	h := NewHandler(gs, issue.NewStore(d.Pool), "")
	r := chi.NewRouter()
	h.Mount(r)
	return r, gs
}

func mintToken(s *Store, c GuestClaims) string {
	if c.ExpiresUnix == 0 {
		c.ExpiresUnix = time.Now().Add(time.Hour).Unix()
	}
	return s.signClaims(&c)
}

func postComment(h http.Handler, wsID, issueID, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST",
		"/guest/workspaces/"+wsID+"/issues/"+issueID+"/comments", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func commentCount(t *testing.T, d *testutil.DB, issueID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM comments WHERE issue_id=$1`, issueID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// (a) ROLE GATE (red-first the dead-code wiring): a VIEWER is blocked by the now-LIVE AllowWrite (403, zero
// comments); a COMMENTER creates a comment (201, author_id = the guest id); an EDITOR also passes (⊇ commenter).
func TestGuestComment_RoleGate(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	iss := d.Issue(t, ws.ID, team.ID)
	h, gs := guestChain(d)

	// viewer → 403, nothing written
	viewer := mintToken(gs, GuestClaims{GuestID: "g-viewer", WorkspaceID: ws.ID, Role: GuestRoleViewer})
	rr := postComment(h, ws.ID, iss.ID, viewer, `{"body":"nope"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("viewer comment = %d, want 403 (AllowWrite must block); body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "INSUFFICIENT_ROLE") {
		t.Fatalf("viewer deny should be INSUFFICIENT_ROLE: %s", rr.Body.String())
	}
	if n := commentCount(t, d, iss.ID); n != 0 {
		t.Fatalf("viewer wrote %d comments, want 0", n)
	}

	// commenter → 201, author_id = the guest id
	commenter := mintToken(gs, GuestClaims{GuestID: "g-commenter", WorkspaceID: ws.ID, Role: GuestRoleCommenter})
	rr = postComment(h, ws.ID, iss.ID, commenter, `{"body":"hello from a guest"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("commenter comment = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if n := commentCount(t, d, iss.ID); n != 1 {
		t.Fatalf("commenter wrote %d comments, want 1", n)
	}

	// editor → 201 (editor ⊇ commenter)
	editor := mintToken(gs, GuestClaims{GuestID: "g-editor", WorkspaceID: ws.ID, Role: GuestRoleEditor})
	rr = postComment(h, ws.ID, iss.ID, editor, `{"body":"editor comment"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("editor comment = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

// (b) TENANCY — cross-workspace: a guest whose token workspace is B posts to URL wsID=A → 403 WS_MISMATCH,
// zero comments. The token-vs-URL guard.
func TestGuestComment_CrossWorkspace_403(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	iss := d.Issue(t, wsA.ID, teamA.ID)
	h, gs := guestChain(d)

	// token for B, posting to A's URL
	tok := mintToken(gs, GuestClaims{GuestID: "g-b", WorkspaceID: wsB.ID, Role: GuestRoleCommenter})
	rr := postComment(h, wsA.ID, iss.ID, tok, `{"body":"cross-tenant"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace comment = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if n := commentCount(t, d, iss.ID); n != 0 {
		t.Fatalf("cross-workspace wrote %d comments, want 0", n)
	}
}

// (c) TENANCY — object: a project-scoped guest cannot comment on an out-of-project issue; and a guest cannot
// comment on an issue outside its workspace (issue in B, token+URL for A). Both 403, zero comments.
func TestGuestComment_ObjectTenancy_403(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA, teamB := d.Team(t, wsA.ID), d.Team(t, wsB.ID)
	issA := d.Issue(t, wsA.ID, teamA.ID) // no project
	issB := d.Issue(t, wsB.ID, teamB.ID)
	h, gs := guestChain(d)

	// project-scoped guest (project P) → issue has no project → 403 PROJECT_MISMATCH
	projTok := mintToken(gs, GuestClaims{GuestID: "g-proj", WorkspaceID: wsA.ID, ProjectID: "proj-P", Role: GuestRoleCommenter})
	rr := postComment(h, wsA.ID, issA.ID, projTok, `{"body":"wrong project"}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "PROJECT_MISMATCH") {
		t.Fatalf("out-of-project comment = %d (%s), want 403 PROJECT_MISMATCH", rr.Code, rr.Body.String())
	}

	// workspace-A guest, URL A, but the issue id is B's → object-in-workspace check → 403 WS_MISMATCH
	aTok := mintToken(gs, GuestClaims{GuestID: "g-a", WorkspaceID: wsA.ID, Role: GuestRoleCommenter})
	rr = postComment(h, wsA.ID, issB.ID, aTok, `{"body":"other ws object"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace object comment = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if commentCount(t, d, issA.ID)+commentCount(t, d, issB.ID) != 0 {
		t.Fatal("object-tenancy denials must write zero comments")
	}
}

// (d) THE GUEST-ACTOR PROOF: a commenter's comment persists with author_id = claims.GuestID (not empty, not a
// member id). The member-attributed notifier is NOT wired into the guest handler (option i) — so no
// member-assuming hook ever receives a guest/empty actor. Here we assert the durable guest attribution.
func TestGuestComment_GuestActorAttribution(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	iss := d.Issue(t, ws.ID, team.ID)
	h, gs := guestChain(d)

	tok := mintToken(gs, GuestClaims{GuestID: "guest-actor-xyz", WorkspaceID: ws.ID, Role: GuestRoleCommenter})
	rr := postComment(h, ws.ID, iss.ID, tok, `{"body":"attributed to the guest"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("comment = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	var author, body string
	if err := d.Pool.QueryRow(ctx,
		`SELECT author_id, body FROM comments WHERE issue_id=$1`, iss.ID).Scan(&author, &body); err != nil {
		t.Fatal(err)
	}
	if author != "guest-actor-xyz" {
		t.Fatalf("author_id = %q, want the GuestID guest-actor-xyz (not empty, not a member id)", author)
	}
	if author == "" {
		t.Fatal("author_id is empty — a member-assuming actor path nil-flowed")
	}
	if body != "attributed to the guest" {
		t.Fatalf("body = %q", body)
	}
	// The client cannot smuggle an author: the handler decodes only {body}, and httpx.DecodeJSON is strict
	// (DisallowUnknownFields), so a body carrying author_id is REJECTED at decode (400) — never used. Even
	// stronger than "ignored": the field can't be sent at all.
	rr = postComment(h, ws.ID, iss.ID, tok, `{"body":"x","author_id":"member-spoof"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a body carrying author_id must be rejected by strict decode = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var spoofed int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE author_id='member-spoof'`).Scan(&spoofed)
	if spoofed != 0 {
		t.Fatal("no comment should ever carry a client-supplied author_id")
	}
}
