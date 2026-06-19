package dependency_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/testutil"
)

// TestCreate_ObjectGraph_RejectsCrossWorkspaceRelation — a relation must link two
// issues in the SAME workspace; creating one across a workspace boundary is the
// write-side root that lets cross-workspace relations (and their read leak) exist.
// Covers both Create and the bulk path. Real Postgres via the harness.
func TestCreate_ObjectGraph_RejectsCrossWorkspaceRelation(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	dep := dependency.NewStore(d.Pool)

	wsA := d.Workspace(t)
	issueA1 := d.Issue(t, wsA.ID, "")
	issueA2 := d.Issue(t, wsA.ID, "")
	wsB := d.Workspace(t)
	issueB := d.Issue(t, wsB.ID, "")

	// Positive control: a same-workspace relation still creates.
	if _, err := dep.Create(ctx, dependency.Relation{
		SourceID: issueA1.ID, TargetID: issueA2.ID, Type: dependency.RelationRelates,
		WorkspaceID: wsA.ID, CreatedBy: "u",
	}); err != nil {
		t.Fatalf("same-workspace relation must create: %v", err)
	}

	// Create across a workspace boundary must be refused.
	if _, err := dep.Create(ctx, dependency.Relation{
		SourceID: issueA1.ID, TargetID: issueB.ID, Type: dependency.RelationRelates,
		WorkspaceID: wsA.ID, CreatedBy: "u",
	}); err == nil {
		t.Error("LEAK: a relation between a WS-A issue and a WS-B issue was created")
	}

	// The bulk path must refuse a cross-workspace target too.
	if _, err := dep.BulkCreateRelations(ctx, dependency.Relation{
		SourceID: issueA1.ID, Type: dependency.RelationRelates, WorkspaceID: wsA.ID, CreatedBy: "u",
	}, []string{issueB.ID}); err == nil {
		t.Error("LEAK: bulk created a cross-workspace relation")
	}
}
