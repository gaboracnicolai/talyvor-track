package featureboard_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/featureboard"
	"github.com/talyvor/track/internal/testutil"
)

// TestVote_ObjectGraph_RejectsPostNotOnPublishedBoard — the public vote path must
// refuse to mutate a post that doesn't belong to the PUBLISHED board named in the
// URL (object-graph integrity), while a legitimate vote on a published board's own
// post still works. Real Postgres via the testutil harness; seeds two workspaces so
// the cross-boundary denial is real.
func TestVote_ObjectGraph_RejectsPostNotOnPublishedBoard(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	fb := featureboard.NewStore(d.Pool)

	// Workspace A: a PUBLIC board with its own post.
	wsA := d.Workspace(t)
	pubBoard, err := fb.CreateBoard(ctx, featureboard.Board{
		WorkspaceID: wsA.ID, Name: "Feedback", Slug: "feedback", Public: true, AllowAnonymous: true,
	})
	if err != nil {
		t.Fatalf("create public board: %v", err)
	}
	pubPost, err := fb.CreatePost(ctx, featureboard.FeaturePost{
		WorkspaceID: wsA.ID, BoardID: pubBoard.ID, Title: "Dark mode", AuthorEmail: "author@a.com",
	})
	if err != nil {
		t.Fatalf("create public post: %v", err)
	}

	// Workspace B: an UNPUBLISHED board with a post.
	wsB := d.Workspace(t)
	privBoard, err := fb.CreateBoard(ctx, featureboard.Board{
		WorkspaceID: wsB.ID, Name: "Private", Slug: "private", Public: false, AllowAnonymous: true,
	})
	if err != nil {
		t.Fatalf("create private board: %v", err)
	}
	privPost, err := fb.CreatePost(ctx, featureboard.FeaturePost{
		WorkspaceID: wsB.ID, BoardID: privBoard.ID, Title: "Secret roadmap item", AuthorEmail: "author@b.com",
	})
	if err != nil {
		t.Fatalf("create private post: %v", err)
	}

	// (1) Positive control: a legit vote on the published board's OWN post works.
	if _, err := fb.Vote(ctx, wsA.Slug, pubBoard.Slug, pubPost.ID, "voter@x.com"); err != nil {
		t.Fatalf("legit vote on a published board must succeed: %v", err)
	}

	// (2) A post on an UNPUBLISHED board must not be votable.
	if _, err := fb.Vote(ctx, wsB.Slug, privBoard.Slug, privPost.ID, "voter@x.com"); err == nil {
		t.Error("LEAK: vote on a post on an UNPUBLISHED board was allowed")
	}

	// (3) A post NOT belonging to the slug's board (cross-board) must be refused —
	//     privPost belongs to wsB/private, not wsA/feedback.
	if _, err := fb.Vote(ctx, wsA.Slug, pubBoard.Slug, privPost.ID, "voter@x.com"); err == nil {
		t.Error("LEAK: vote on a post not belonging to the slug's board was allowed")
	}

	// (4) Unvote is guarded the same way.
	if _, err := fb.Unvote(ctx, wsB.Slug, privBoard.Slug, privPost.ID, "voter@x.com"); err == nil {
		t.Error("LEAK: unvote on an unpublished board's post was allowed")
	}
}
