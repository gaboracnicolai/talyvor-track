package issue_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// ITEM A: store.BulkUpdate had no workspace scoping — its per-item SQL was
// `UPDATE issues SET ... WHERE id = $N` (no workspace_id), so a member of workspace A could flip the
// status/sort_order of ANY workspace's issues by id. This MUST be a real-PG test: pgxmock masks the hole
// (it returns whatever RowsAffected you script, so an unscoped UPDATE "passes" against a mock).
//
// RED (current code): a ws-A bulk update mutates a ws-B issue. GREEN (after the workspace_id predicate):
// the foreign id matches 0 rows → silent skip, count excludes it, the issue is untouched.
func TestBulkUpdate_RefusesForeignWorkspaceIssue(t *testing.T) {
	ctx := context.Background()
	d := testutil.New(t) // SKIPs without TRACK_TEST_DATABASE_URL

	wsA := d.Workspace(t)
	wsB := d.Workspace(t)
	if wsA.ID == "" || wsA.ID == wsB.ID {
		t.Fatalf("workspaces not isolated: A=%q B=%q", wsA.ID, wsB.ID)
	}
	issB := d.Issue(t, wsB.ID, "") // issue owned by workspace B (default status 'backlog')

	store := issue.NewStore(d.Pool)

	// As workspace A, attempt to cancel workspace B's issue by its id.
	count, err := store.BulkUpdate(ctx, wsA.ID, []issue.BulkUpdateItem{{ID: issB.ID, Status: "cancelled"}})
	if err != nil {
		t.Fatalf("BulkUpdate: %v", err)
	}

	if count != 0 {
		t.Errorf("BulkUpdate as ws-A reported updated=%d for a ws-B issue — want 0 (foreign id must silently skip)", count)
	}
	got, err := store.GetByID(ctx, issB.ID)
	if err != nil {
		t.Fatalf("GetByID issB: %v", err)
	}
	if got.Status == "cancelled" {
		t.Errorf("SECURITY (cross-tenant IDOR): ws-A BulkUpdate MUTATED ws-B issue %s → status=%q", issB.ID, got.Status)
	}
}
