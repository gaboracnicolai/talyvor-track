package featureboard_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/featureboard"
	"github.com/talyvor/track/internal/testutil"
)

// TestUpdateStatus_ObjectGraph_RejectsCrossWorkspace — UpdateStatus must refuse to
// mutate a post outside the given board/workspace, and must refuse to link an issue
// from another workspace, while a legit same-workspace status change still works.
// Real Postgres via the harness; the linked issue is seeded in a second workspace.
func TestUpdateStatus_ObjectGraph_RejectsCrossWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	fb := featureboard.NewStore(d.Pool)

	wsA := d.Workspace(t)
	boardA, err := fb.CreateBoard(ctx, featureboard.Board{
		WorkspaceID: wsA.ID, Name: "Roadmap", Slug: "roadmap", Public: true, AllowAnonymous: true,
	})
	if err != nil {
		t.Fatalf("create board: %v", err)
	}
	postA, err := fb.CreatePost(ctx, featureboard.FeaturePost{
		WorkspaceID: wsA.ID, BoardID: boardA.ID, Title: "Item", AuthorEmail: "a@a.com",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	wsB := d.Workspace(t)
	issueB := d.Issue(t, wsB.ID, "")

	planned := featureboard.PostStatusPlanned

	// Positive control: a same-workspace/board status change works.
	if err := fb.UpdateStatus(ctx, wsA.ID, boardA.ID, postA.ID, planned, nil); err != nil {
		t.Fatalf("same-workspace UpdateStatus must succeed: %v", err)
	}
	// Cross-workspace post mutation refused (post is in wsA, not wsB).
	if err := fb.UpdateStatus(ctx, wsB.ID, boardA.ID, postA.ID, planned, nil); err == nil {
		t.Error("LEAK: a post was mutated from a different workspace")
	}
	// Wrong board refused (post belongs to boardA).
	if err := fb.UpdateStatus(ctx, wsA.ID, "no-such-board", postA.ID, planned, nil); err == nil {
		t.Error("LEAK: a post was mutated via the wrong board")
	}
	// Cross-workspace issue link refused (issueB is in wsB).
	if err := fb.UpdateStatus(ctx, wsA.ID, boardA.ID, postA.ID, planned, &issueB.ID); err == nil {
		t.Error("LEAK: a post was linked to an issue in a different workspace")
	}
}
