package issue_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// THE LIVE CROSS-TENANT WRITE IDOR (root cause: store.CreateComment did not scope the
// parent issue to the caller's authorized workspace). The /v1 route
// POST /v1/workspaces/{wsID}/issues/{id}/comments authorizes the caller for {wsID} but the
// handler passed {id} straight to the unscoped INSERT. A member of wsA — authorized for wsA
// in the path — could write a comment onto wsB's issue.
//
// RED on current main: the write SUCCEEDS (201, a comment row lands on wsB's issue).
// GREEN after the fix: the store scopes the insert to the parent issue's workspace → 404
// (no-oracle: a foreign and a nonexistent issue are indistinguishable), 0 rows written.
func TestSEC_CreateComment_CrossWorkspace_Rejected(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	sec5Member(t, d, wsA.ID, "alice@corp.com") // alice is a member of wsA ONLY
	teamB := d.Team(t, wsB.ID)
	issueB := d.Issue(t, wsB.ID, teamB.ID) // the target issue lives in wsB

	chain := sec5IdentityChain(d)
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, postJSONAs(wsA.ID, "/issues/"+issueB.ID+"/comments", "alice@corp.com",
		`{"body":"cross-tenant write"}`))

	if rr.Code == http.StatusCreated {
		t.Errorf("LIVE IDOR: a wsA member wrote a comment onto wsB's issue (HTTP %d) — cross-tenant write", rr.Code)
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-tenant comment must be 404 (no-oracle); got %d: %s", rr.Code, rr.Body.String())
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE issue_id=$1`, issueB.ID).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if n != 0 {
		t.Errorf("a comment landed on wsB's issue (%d rows) — the cross-tenant write was not prevented", n)
	}
}

// Same-workspace happy path — the fix must not break a legitimate comment: a wsA member
// commenting on a wsA issue → 201, comment lands.
func TestSEC_CreateComment_SameWorkspace_Succeeds(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA := d.Workspace(t)
	sec5Member(t, d, wsA.ID, "alice@corp.com")
	teamA := d.Team(t, wsA.ID)
	issueA := d.Issue(t, wsA.ID, teamA.ID)

	chain := sec5IdentityChain(d)
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, postJSONAs(wsA.ID, "/issues/"+issueA.ID+"/comments", "alice@corp.com",
		`{"body":"legit"}`))

	if rr.Code != http.StatusCreated {
		t.Fatalf("same-workspace comment must succeed (201); got %d: %s", rr.Code, rr.Body.String())
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE issue_id=$1`, issueA.ID).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if n != 1 {
		t.Errorf("same-workspace comment did not land (%d rows)", n)
	}
}

// Store-level lock for the primitive: a foreign issue AND a nonexistent issue both return
// ErrNotFound (no oracle distinguishing them), while an own-workspace issue lands.
func TestSEC_CreateComment_Store_ScopedToWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA, teamB := d.Team(t, wsA.ID), d.Team(t, wsB.ID)
	issueA := d.Issue(t, wsA.ID, teamA.ID)
	issueB := d.Issue(t, wsB.ID, teamB.ID)
	store := issue.NewStore(d.Pool)

	if _, err := store.CreateComment(ctx, model.Comment{IssueID: issueB.ID, AuthorID: "m-a", Body: "x"}, wsA.ID); !errors.Is(err, issue.ErrNotFound) {
		t.Errorf("comment on a FOREIGN issue must be ErrNotFound; got %v", err)
	}
	if _, err := store.CreateComment(ctx, model.Comment{IssueID: "does-not-exist", AuthorID: "m-a", Body: "x"}, wsA.ID); !errors.Is(err, issue.ErrNotFound) {
		t.Errorf("comment on a NONEXISTENT issue must be ErrNotFound (no oracle); got %v", err)
	}
	c, err := store.CreateComment(ctx, model.Comment{IssueID: issueA.ID, AuthorID: "m-a", Body: "ok"}, wsA.ID)
	if err != nil || c == nil {
		t.Fatalf("comment on an OWN-workspace issue must succeed; got %v", err)
	}
}
