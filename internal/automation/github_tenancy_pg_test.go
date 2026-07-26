package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// AUDIT FINDING (cross-tenant write): issue.Store.GetByIdentifier ran
//
//	SELECT ... FROM issues WHERE identifier = $1
//
// with no workspace filter and no LIMIT. Migration 0022 made identifier unique PER
// WORKSPACE, not globally — two tenants each running a team called ENG both hold ENG-1.
//
// The GitHub webhook resolved PR references through that lookup and then WROTE: it set
// the issue to done and posted a comment. TRACK_GITHUB_WEBHOOK_SECRET is a single global
// secret, so any GitHub org pointed at the deployment could close and comment on any
// tenant's issues just by naming an identifier — and which tenant it hit was whatever row
// Postgres returned first.
//
// This test uses REAL Postgres deliberately: the defect lives in the SQL, and a fake
// issue lookup keyed by a Go map cannot express "two workspaces, same identifier".

const ghTenancySecret = "gh-tenancy-test-secret"

// seedIdentifierCollision creates two workspaces that both hold an issue with the SAME
// identifier — the situation migration 0022 permits and the unscoped lookup could not
// distinguish. Returns (victimWorkspaceID, victimIssueID, otherWorkspaceID, otherIssueID)
// and the shared identifier.
func seedIdentifierCollision(t *testing.T, d *testutil.DB) (victimWS, victimIssue, otherWS, otherIssue, identifier string) {
	t.Helper()
	ctx := context.Background()
	store := issue.NewStore(d.Pool)

	mk := func(label string) (wsID, issueID, ident string) {
		var w string
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO workspaces (name, slug) VALUES ($1,$2) RETURNING id`,
			label, label).Scan(&w); err != nil {
			t.Fatalf("seed workspace %s: %v", label, err)
		}
		var tm string
		// The SAME team identifier in both workspaces is what produces the collision:
		// teams.UNIQUE is (workspace_id, identifier), so "ENG" is free in each.
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO teams (workspace_id, name, identifier) VALUES ($1,'Engineering','ENG') RETURNING id`,
			w).Scan(&tm); err != nil {
			t.Fatalf("seed team %s: %v", label, err)
		}
		created, err := store.Create(ctx, model.Issue{
			WorkspaceID: w, TeamID: tm, Title: label + " work", CreatorID: "seed",
		})
		if err != nil {
			t.Fatalf("seed issue %s: %v", label, err)
		}
		return w, created.ID, created.Identifier
	}

	victimWS, victimIssue, victimIdent := mk("victim")
	otherWS, otherIssue, otherIdent := mk("attacker")
	if victimIdent != otherIdent {
		t.Fatalf("fixture did not collide: %q vs %q — the test cannot exercise the defect", victimIdent, otherIdent)
	}
	return victimWS, victimIssue, otherWS, otherIssue, victimIdent
}

func statusOf(t *testing.T, d *testutil.DB, issueID string) string {
	t.Helper()
	var s string
	if err := d.Pool.QueryRow(context.Background(), `SELECT status FROM issues WHERE id=$1`, issueID).Scan(&s); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s
}

func commentCount(t *testing.T, d *testutil.DB, issueID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM comments WHERE issue_id=$1`, issueID).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	return n
}

func prMergedBody(t *testing.T, ref string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"number": 7,
			"title":  fmt.Sprintf("Closes %s", ref),
			"body":   "",
			"merged": true,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return b
}

// RED before the fix: the webhook closed an issue in a workspace it was never scoped to.
// GREEN after: with no configured workspace the handler refuses to act on ANY issue.
func TestGitHub_UnscopedWebhook_CannotCloseAnyTenantsIssue(t *testing.T) {
	d := testutil.New(t)
	victimWS, victimIssue, _, otherIssue, ident := seedIdentifierCollision(t, d)
	_ = victimWS

	// No workspace configured — the deployment has not opted this webhook into a tenant.
	h := NewGitHubHandler(nil, issue.NewStore(d.Pool), ghTenancySecret)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, signedGitHubReq(t, ghTenancySecret, "pull_request", prMergedBody(t, ident)))
	if rr.Code != http.StatusOK {
		t.Fatalf("webhook = %d, want 200 (an authentic delivery is still acknowledged)", rr.Code)
	}

	for name, id := range map[string]string{"victim": victimIssue, "other": otherIssue} {
		if got := statusOf(t, d, id); got == "done" {
			t.Fatalf("CROSS-TENANT WRITE: %s workspace's %s was closed by a webhook with no workspace scope (status=%q)",
				name, ident, got)
		}
		if n := commentCount(t, d, id); n != 0 {
			t.Fatalf("CROSS-TENANT WRITE: %s workspace's %s received %d webhook comment(s) with no workspace scope",
				name, ident, n)
		}
	}
}

// The configured workspace bounds the blast radius: the webhook acts on ITS tenant's
// issue and cannot touch the identically-identified issue next door.
//
// It configures the SECOND-seeded workspace deliberately. The unscoped lookup returned
// whichever row Postgres produced first — which is the FIRST-seeded one — so scoping to
// the second is what makes this test discriminate. Scoping to the first would have passed
// even against the broken code, by coincidence, and pinned nothing. (Observed: it did.)
func TestGitHub_ScopedWebhook_ActsOnlyOnItsOwnWorkspace(t *testing.T) {
	d := testutil.New(t)
	_, firstSeededIssue, configuredWS, configuredIssue, ident := seedIdentifierCollision(t, d)

	h := NewGitHubHandler(nil, issue.NewStore(d.Pool), ghTenancySecret).WithWorkspace(configuredWS)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, signedGitHubReq(t, ghTenancySecret, "pull_request", prMergedBody(t, ident)))
	if rr.Code != http.StatusOK {
		t.Fatalf("webhook = %d, want 200", rr.Code)
	}

	if got := statusOf(t, d, configuredIssue); got != "done" {
		t.Fatalf("configured workspace's %s status = %q, want done — the scoped webhook must still work", ident, got)
	}
	if got := statusOf(t, d, firstSeededIssue); got == "done" {
		t.Fatalf("CROSS-TENANT WRITE: the OTHER workspace's %s was closed instead of / as well as the configured one (status=%q)",
			ident, got)
	}
	if n := commentCount(t, d, firstSeededIssue); n != 0 {
		t.Fatalf("CROSS-TENANT WRITE: the OTHER workspace's %s received %d comment(s)", ident, n)
	}
}

// LAYERING (found by positive-controlling the test above): removing handlePullRequest's
// fail-closed early return did NOT turn TestGitHub_UnscopedWebhook red, because
// issue.Store.GetByIdentifier rejects an empty workspace on its own and handleMerged
// skips on the error. That test therefore pins the END-TO-END property ("no scope ⇒ no
// write") while the store is what actually enforces it — so the handler's guard had no
// test of its own and could have been deleted silently.
//
// This pins the handler layer directly: with no workspace configured it must not reach
// the store AT ALL. Two independent guards, two independent tests.
func TestGitHub_UnscopedWebhook_ShortCircuitsBeforeTouchingTheStore(t *testing.T) {
	fake := &fakeIssueLookup{issuesByIdentifier: map[string]*model.Issue{
		"ENG-42": {ID: "i-1", Identifier: "ENG-42", WorkspaceID: "ws-1"},
	}}
	h := NewGitHubHandler(nil, fake, ghTenancySecret) // deliberately NO WithWorkspace

	body := []byte(`{"action":"closed","pull_request":{"number":7,"title":"Fixes ENG-42","body":"","merged":true}}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, signedGitHubReq(t, ghTenancySecret, "pull_request", body))

	if rr.Code != http.StatusOK {
		t.Fatalf("webhook = %d, want 200", rr.Code)
	}
	if fake.lookups != 0 {
		t.Fatalf("handler made %d issue lookup(s) with no workspace scope — it must short-circuit "+
			"before the store, not rely on the store rejecting an empty workspace", fake.lookups)
	}
	if len(fake.updates) != 0 || len(fake.comments) != 0 {
		t.Fatalf("unscoped webhook produced %d update(s) and %d comment(s)", len(fake.updates), len(fake.comments))
	}
}
