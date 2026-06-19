package testutil_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/customfield"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// TestHarness_Sanity_RealPostgres is the GREEN proof that the integration harness
// works end-to-end against real Postgres: two isolated workspaces, an issue seeded
// in each, a workspace-scoped read returns that workspace's OWN issue, and the seed
// helpers (issue / comment / custom-field) round-trip. This is NOT a security test —
// cross-tenant denial tests arrive paired with their fixes in later scoping PRs.
func TestHarness_Sanity_RealPostgres(t *testing.T) {
	d := testutil.New(t) // skips cleanly when TRACK_TEST_DATABASE_URL is unset
	ctx := context.Background()

	wsA := d.Workspace(t)
	wsB := d.Workspace(t)
	if wsA.ID == "" || wsA.ID == wsB.ID {
		t.Fatalf("workspaces not isolated: A=%q B=%q", wsA.ID, wsB.ID)
	}

	issA := d.Issue(t, wsA.ID, "")
	issB := d.Issue(t, wsB.ID, "")
	if issA.ID == issB.ID {
		t.Fatal("seeded issues collided across workspaces")
	}

	store := issue.NewStore(d.Pool)

	// Reading within workspace A returns A's own issue (List is workspace-scoped).
	listA, err := store.List(ctx, issue.IssueFilter{WorkspaceID: wsA.ID})
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if len(listA) != 1 || listA[0].ID != issA.ID {
		t.Fatalf("workspace A list = %d issue(s), want exactly issA (%s)", len(listA), issA.ID)
	}

	// Basic CRUD round-trip: GetByID returns the seeded issue with fields intact.
	got, err := store.GetByID(ctx, issA.ID)
	if err != nil {
		t.Fatalf("get issA: %v", err)
	}
	if got.Title != issA.Title || got.WorkspaceID != wsA.ID {
		t.Fatalf("issue round-trip mismatch: got title=%q ws=%q", got.Title, got.WorkspaceID)
	}

	// Comment seed helper round-trips.
	c := d.Comment(t, issA.ID, "first comment")
	comments, err := store.ListComments(ctx, issA.ID)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != c.ID || comments[0].Body != "first comment" {
		t.Fatalf("comment round-trip failed: %+v", comments)
	}

	// Custom-field seed helpers round-trip.
	f := d.CustomField(t, wsA.ID, "Severity")
	d.SetFieldValue(t, issA.ID, f.ID, "high")
	vals, err := customfield.NewStore(d.Pool).GetValues(ctx, issA.ID)
	if err != nil {
		t.Fatalf("get field values: %v", err)
	}
	if vals[f.ID] != "high" {
		t.Fatalf("custom-field round-trip failed: got %v, want {%s: high}", vals, f.ID)
	}
}
