package issue_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/testutil"
)

// seedRoleMember inserts a member with an explicit role and returns its member id (the
// value the chain resolves as the caller's actor — so a comment authored by this id makes
// the caller "the author").
func seedRoleMember(t *testing.T, d *testutil.DB, wsID, email, role string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,$4) RETURNING id`,
		wsID, email, email, role).Scan(&id); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return id
}

func seedCommentBy(t *testing.T, d *testutil.DB, issueID, authorID, body string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO comments (issue_id, author_id, body) VALUES ($1,$2,$3) RETURNING id`,
		issueID, authorID, body).Scan(&id); err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	return id
}

func commentExists(t *testing.T, d *testutil.DB, id string) bool {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM comments WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	return n > 0
}

// DELETE comment: only the AUTHOR or a workspace OWNER. A non-author member is refused
// (404, no-oracle) and the comment survives; the author deletes their own; an owner deletes
// anyone's.
func TestComment_Delete_AuthorOrOwner(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	aliceID := seedRoleMember(t, d, ws.ID, "alice@x.com", "member")
	seedRoleMember(t, d, ws.ID, "bob@x.com", "member")  // non-author member
	seedRoleMember(t, d, ws.ID, "carol@x.com", "owner") // owner
	iss := d.Issue(t, ws.ID, "")
	chain := sec5IdentityChain(d)

	// Non-author member → refused, comment survives.
	c1 := seedCommentBy(t, d, iss.ID, aliceID, "alice's comment")
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, delAs(ws.ID, "/issues/"+iss.ID+"/comments/"+c1, "bob@x.com"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-author delete = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	if !commentExists(t, d, c1) {
		t.Fatal("a non-author deleted another member's comment")
	}

	// Author → allowed.
	rr = httptest.NewRecorder()
	chain.ServeHTTP(rr, delAs(ws.ID, "/issues/"+iss.ID+"/comments/"+c1, "alice@x.com"))
	if rr.Code != http.StatusOK || commentExists(t, d, c1) {
		t.Fatalf("author delete = %d (exists=%v), want 200 and gone", rr.Code, commentExists(t, d, c1))
	}

	// Owner → allowed on someone else's comment.
	c2 := seedCommentBy(t, d, iss.ID, aliceID, "alice's second")
	rr = httptest.NewRecorder()
	chain.ServeHTTP(rr, delAs(ws.ID, "/issues/"+iss.ID+"/comments/"+c2, "carol@x.com"))
	if rr.Code != http.StatusOK || commentExists(t, d, c2) {
		t.Fatalf("owner delete = %d (exists=%v), want 200 and gone", rr.Code, commentExists(t, d, c2))
	}
}

// PATCH comment (edit body): same author-or-owner rule.
func TestComment_Edit_AuthorOrOwner(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	aliceID := seedRoleMember(t, d, ws.ID, "alice@x.com", "member")
	seedRoleMember(t, d, ws.ID, "bob@x.com", "member")
	seedRoleMember(t, d, ws.ID, "carol@x.com", "owner")
	iss := d.Issue(t, ws.ID, "")
	chain := sec5IdentityChain(d)

	bodyOf := func(id string) string {
		var b string
		if err := d.Pool.QueryRow(context.Background(), `SELECT body FROM comments WHERE id=$1`, id).Scan(&b); err != nil {
			t.Fatalf("read body: %v", err)
		}
		return b
	}

	c := seedCommentBy(t, d, iss.ID, aliceID, "orig")

	// Non-author member → refused, body unchanged.
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, patchAs(ws.ID, "/issues/"+iss.ID+"/comments/"+c, "bob@x.com", `{"body":"hijacked"}`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("non-author edit = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	if bodyOf(c) != "orig" {
		t.Fatalf("a non-author edited another's comment: body=%q", bodyOf(c))
	}

	// Author → allowed.
	rr = httptest.NewRecorder()
	chain.ServeHTTP(rr, patchAs(ws.ID, "/issues/"+iss.ID+"/comments/"+c, "alice@x.com", `{"body":"by-author"}`))
	if rr.Code != http.StatusOK || bodyOf(c) != "by-author" {
		t.Fatalf("author edit = %d, body=%q, want 200/by-author", rr.Code, bodyOf(c))
	}

	// Owner → allowed.
	rr = httptest.NewRecorder()
	chain.ServeHTTP(rr, patchAs(ws.ID, "/issues/"+iss.ID+"/comments/"+c, "carol@x.com", `{"body":"by-owner"}`))
	if rr.Code != http.StatusOK || bodyOf(c) != "by-owner" {
		t.Fatalf("owner edit = %d, body=%q, want 200/by-owner", rr.Code, bodyOf(c))
	}
}
