package dependency_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/testutil"
)

// TestGetDependencyGraph_ObjectGraph_NoCrossWorkspaceNode — the graph must not
// surface a node (issue content) from another workspace, nor accept a root issue
// outside the queried workspace. The edge-expansion query IS workspace-scoped, but a
// raw/legacy relation stamped with the queried workspace can still point at a foreign
// issue; the node-metadata fetch (and the root fetch) must be scoped too. Real PG.
func TestGetDependencyGraph_ObjectGraph_NoCrossWorkspaceNode(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	dep := dependency.NewStore(d.Pool)

	wsA := d.Workspace(t)
	root := d.Issue(t, wsA.ID, "")
	neighbor := d.Issue(t, wsA.ID, "") // same-workspace node (control)
	wsB := d.Workspace(t)
	foreign := d.Issue(t, wsB.ID, "")

	// Legit same-workspace relation root -> neighbor.
	if _, err := dep.Create(ctx, dependency.Relation{
		SourceID: root.ID, TargetID: neighbor.ID, Type: dependency.RelationRelates,
		WorkspaceID: wsA.ID, CreatedBy: "u",
	}); err != nil {
		t.Fatalf("create same-workspace relation: %v", err)
	}
	// Raw cross-workspace relation root -> foreign, stamped with wsA (Create now
	// refuses one; this is legacy/raw). workspace_id = wsA, so the workspace-scoped
	// edge expansion still traverses it — the leak would be the node-metadata fetch.
	if _, err := d.Pool.Exec(ctx,
		`INSERT INTO issue_relations (source_id, target_id, type, workspace_id, created_by)
         VALUES ($1, $2, 'relates_to', $3, 'u')`,
		root.ID, foreign.ID, wsA.ID,
	); err != nil {
		t.Fatalf("seed raw cross-workspace relation: %v", err)
	}

	graph, err := dep.GetDependencyGraph(ctx, wsA.ID, root.ID, 3)
	if err != nil {
		t.Fatalf("GetDependencyGraph: %v", err)
	}
	var sawForeign, sawNeighbor bool
	for _, n := range graph.Nodes {
		if n.ID == foreign.ID {
			sawForeign = true
		}
		if n.ID == neighbor.ID {
			sawNeighbor = true
		}
	}
	if sawForeign {
		t.Error("LEAK: GetDependencyGraph surfaced a cross-workspace node's content")
	}
	if !sawNeighbor {
		t.Error("same-workspace neighbor node must still appear")
	}

	// The root must belong to the queried workspace (root-fetch scope): asking for a
	// WS-A issue under WS-B must not return its content.
	if g2, err := dep.GetDependencyGraph(ctx, wsB.ID, root.ID, 1); err == nil {
		for _, n := range g2.Nodes {
			if n.ID == root.ID {
				t.Error("LEAK: GetDependencyGraph returned a root issue from another workspace")
			}
		}
	}
}
