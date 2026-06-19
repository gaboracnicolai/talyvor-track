package dependency_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/testutil"
)

// TestBlockedBy_ObjectGraph_NoCrossWorkspaceSurfacing — GetBlockedBy and IsBlocked
// must not surface or count a blocker that lives in a different workspace, and
// GetBlockedBy must not crash on the unqualified-workspace_id ambiguity. Real PG;
// the cross-workspace "blocks" row is inserted raw (Create now refuses one).
func TestBlockedBy_ObjectGraph_NoCrossWorkspaceSurfacing(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	dep := dependency.NewStore(d.Pool)

	wsA := d.Workspace(t)
	target := d.Issue(t, wsA.ID, "")      // "blocked" only across a workspace boundary
	legitTarget := d.Issue(t, wsA.ID, "") // positive control
	sameWsBlocker := d.Issue(t, wsA.ID, "")

	wsB := d.Workspace(t)
	crossWsBlocker := d.Issue(t, wsB.ID, "")

	// Legit same-workspace block: sameWsBlocker blocks legitTarget.
	if _, err := dep.Create(ctx, dependency.Relation{
		SourceID: sameWsBlocker.ID, TargetID: legitTarget.ID, Type: dependency.RelationBlocks,
		WorkspaceID: wsA.ID, CreatedBy: "u",
	}); err != nil {
		t.Fatalf("create same-workspace block: %v", err)
	}
	// Cross-workspace block inserted RAW (Create refuses one; models legacy data).
	if _, err := d.Pool.Exec(ctx,
		`INSERT INTO issue_relations (source_id, target_id, type, workspace_id, created_by)
         VALUES ($1, $2, 'blocks', $3, 'u')`,
		crossWsBlocker.ID, target.ID, wsB.ID,
	); err != nil {
		t.Fatalf("seed raw cross-workspace block: %v", err)
	}

	// IsBlocked must NOT count a cross-workspace blocker (COUNT path; never crashes).
	if blocked, err := dep.IsBlocked(ctx, target.ID); err != nil {
		t.Fatalf("IsBlocked: %v", err)
	} else if blocked {
		t.Error("LEAK: IsBlocked counted a cross-workspace blocker")
	}

	// GetBlockedBy must not crash (ambiguous workspace_id) nor surface the foreign
	// blocker's content.
	blocked, err := dep.GetBlockedBy(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetBlockedBy errored (ambiguous-column crash?): %v", err)
	}
	for _, b := range blocked {
		if b.ID == crossWsBlocker.ID || b.WorkspaceID == wsB.ID {
			t.Errorf("LEAK: GetBlockedBy surfaced cross-workspace blocker %s (ws %s)", b.ID, b.WorkspaceID)
		}
	}

	// Positive controls: the same-workspace block still surfaces + counts.
	if lb, err := dep.GetBlockedBy(ctx, legitTarget.ID); err != nil {
		t.Fatalf("GetBlockedBy(legit): %v", err)
	} else if len(lb) != 1 || lb[0].ID != sameWsBlocker.ID {
		t.Errorf("same-workspace blocker must be returned (got %d)", len(lb))
	}
	if b, err := dep.IsBlocked(ctx, legitTarget.ID); err != nil || !b {
		t.Errorf("legit same-workspace block must count (isBlocked=%v err=%v)", b, err)
	}
}
