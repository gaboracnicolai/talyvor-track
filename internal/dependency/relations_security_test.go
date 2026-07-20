package dependency_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/testutil"
)

// TestGetRelations_ObjectGraph_NoCrossWorkspaceContentLeak — GetRelations must not
// return a related issue's content when that issue lives in a different workspace
// (object-graph integrity), while a same-workspace relation still surfaces with its
// content. Real Postgres via the harness; a cross-workspace relation is seeded
// directly (Create does not currently prevent one).
func TestGetRelations_ObjectGraph_NoCrossWorkspaceContentLeak(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	dep := dependency.NewStore(d.Pool)

	wsA := d.Workspace(t)
	issueA := d.Issue(t, wsA.ID, "")
	issueA2 := d.Issue(t, wsA.ID, "") // same-workspace neighbour

	wsB := d.Workspace(t)
	issueB := d.Issue(t, wsB.ID, "") // foreign-workspace issue with its own content

	// Same-workspace relation A -> A2 (legit; must keep surfacing with content).
	if _, err := dep.Create(ctx, dependency.Relation{
		SourceID: issueA.ID, TargetID: issueA2.ID, Type: dependency.RelationRelates,
		WorkspaceID: wsA.ID, CreatedBy: "u",
	}); err != nil {
		t.Fatalf("create same-workspace relation: %v", err)
	}
	// Cross-workspace relation A -> B, inserted RAW: Create now refuses one (the #1
	// write-side fix), so this models pre-existing/legacy data. The read path must
	// still not leak the foreign issue's content.
	if _, err := d.Pool.Exec(ctx,
		`INSERT INTO issue_relations (source_id, target_id, type, workspace_id, created_by)
         VALUES ($1, $2, 'relates_to', $3, 'u')`,
		issueA.ID, issueB.ID, wsA.ID,
	); err != nil {
		t.Fatalf("seed raw cross-workspace relation: %v", err)
	}

	rels, err := dep.GetRelations(ctx, issueA.ID, wsA.ID)
	if err != nil {
		t.Fatalf("get relations: %v", err)
	}

	// The cross-workspace related issue's content must NOT surface.
	for _, r := range rels {
		if r.Issue.ID == issueB.ID || r.Issue.WorkspaceID == wsB.ID {
			t.Errorf("LEAK: GetRelations surfaced cross-workspace issue %s (ws %s) title=%q",
				r.Issue.ID, r.Issue.WorkspaceID, r.Issue.Title)
		}
	}

	// Positive control: the same-workspace relation is still returned with content.
	var foundA2 bool
	for _, r := range rels {
		if r.Issue.ID == issueA2.ID {
			foundA2 = true
			if r.Issue.Title == "" {
				t.Error("same-workspace related issue should still carry its content")
			}
		}
	}
	if !foundA2 {
		t.Error("same-workspace relation must still be returned (fix must not break legit relations)")
	}
}
