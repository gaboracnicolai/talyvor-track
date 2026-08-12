package project_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

// roadmap_completed_divergence_realpg_test.go — TWO SURFACES REPORT "COMPLETED" FOR THE SAME
// ISSUES AND THEY DO NOT AGREE, BECAUSE THE WORD IS SPELLED AS TWO DIFFERENT PREDICATES.
//
//	project/roadmap.go:65   COUNT(i.id) FILTER (WHERE i.status IN ('done','cancelled'))  project rollup
//	project/roadmap.go:123  COUNT(i.id) FILTER (WHERE i.status IN ('done','cancelled'))  milestone rollup
//	milestone/store.go:189  COUNT(*)    FILTER (WHERE status = 'done')                   GetProgress
//	cycle/store.go:258      COUNT(*)    FILTER (WHERE status = 'done')                   GetProgress
//
// A cancelled issue attached to a milestone is COMPLETED on the Roadmap page and NOT COMPLETED in
// that same milestone's progress payload. `MilestoneMarker.tsx` renders the roadmap number as
// "<completed>/<total> done"; the milestone progress endpoint and the MCP tool render the other one.
// Nothing in the repo says the two words mean different things, and no test held them apart, so
// either side could have been "fixed" into agreement with the other by a session that only read one.
//
// ⚠ THIS TEST DECIDES NOTHING AND IS NOT A BUG REPORT ABOUT EITHER PREDICATE. Whether a cancelled
// issue counts as completed is a product question with two defensible answers — "cancelled work is
// off the board, so the milestone is that much closer to closing" and "cancelled work was never
// delivered, so calling it done overstates progress" — and picking one changes a number a user
// reads. It is pinned here as an executable FACT so the divergence is visible to whoever decides,
// and so that changing ONE side turns this red instead of silently making two surfaces agree by
// accident. If a decision is taken, this test is the place to record it: change the expectation and
// say which answer won and where.
//
// ⚠ WHAT MAKES THIS A MEASUREMENT AND NOT A RESTATEMENT OF THE SOURCE: every number below comes out
// of real Postgres through the SHIPPED stores over ONE fixture. The cycle surface is fixtured rather
// than cited, because "cycle/store.go also says done-only" read off a grep is exactly the kind of
// claim that goes stale. The `todo` issue is the anti-vacuity control — without it a query that
// counted every attached row would satisfy the roadmap assertion for the wrong reason.
func TestRoadmapAndMilestoneProgress_DisagreeOnWhetherCancelledIsCompleted_RealPG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.New(t)

	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	issues := issue.NewStore(db.Pool)
	projects := project.NewStore(db.Pool)
	milestones := milestone.NewStore(db.Pool)
	cycles := cycle.NewStore(db.Pool)

	now := time.Now().UTC()
	target := now.Add(30 * 24 * time.Hour)
	proj, err := projects.Create(ctx, model.Project{
		WorkspaceID: ws.ID, TeamID: tm.ID, Name: "Divergence", Identifier: "DIV",
		StartDate: &now, TargetDate: &target,
	})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	ms, err := milestones.Create(ctx, milestone.Milestone{
		WorkspaceID: ws.ID, ProjectID: proj.ID, Name: "Beta", TargetDate: &target,
	})
	if err != nil {
		t.Fatalf("seed milestone: %v", err)
	}
	cyc, err := cycles.Create(ctx, model.Cycle{
		WorkspaceID: ws.ID, TeamID: tm.ID, Name: "Sprint 1",
		StartDate: now, EndDate: target,
	})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}

	// One issue per status that matters: one delivered, one cancelled, one still open. The two
	// predicates differ on exactly the middle one.
	for _, seed := range []struct {
		title  string
		status model.IssueStatus
	}{
		{"delivered", model.StatusDone},
		{"cancelled", model.StatusCancelled},
		{"still open", model.StatusTodo},
	} {
		iss, err := issues.Create(ctx, model.Issue{
			WorkspaceID: ws.ID, TeamID: tm.ID, ProjectID: &proj.ID,
			Title: seed.title, CreatorID: "seed", Status: seed.status,
		})
		if err != nil {
			t.Fatalf("seed issue %q: %v", seed.title, err)
		}
		// Attach through the SHIPPED write path — the one PATCH /v1/issues/{id} reaches.
		if _, err := issues.Update(ctx, iss.ID, ws.ID, map[string]any{
			"milestone_id": ms.ID,
			"cycle_id":     cyc.ID,
		}); err != nil {
			t.Fatalf("attach %q: %v", seed.title, err)
		}
	}

	// (1) THE ROADMAP — done OR cancelled.
	roadmap, err := projects.GetRoadmap(ctx, ws.ID, nil, now.Add(-24*time.Hour), target.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("roadmap: %v", err)
	}
	var marker *project.RoadmapMilestone
	for _, rp := range roadmap {
		for i := range rp.Milestones {
			if rp.Milestones[i].ID == ms.ID {
				marker = &rp.Milestones[i]
			}
		}
	}
	if marker == nil {
		t.Fatalf("roadmap did not return milestone %s at all — fixture or join is broken, not a divergence", ms.ID)
	}
	if marker.IssueCount != 3 {
		t.Fatalf("roadmap issue_count = %d, want 3 — the fixture did not attach what this test assumes", marker.IssueCount)
	}
	if marker.CompletedCount != 2 {
		t.Errorf("roadmap completed_count = %d, want 2 (done + cancelled). "+
			"If this changed deliberately, the milestone and cycle surfaces below must change with it "+
			"or the product now reports three different completion numbers instead of two.",
			marker.CompletedCount)
	}

	// (2) THE MILESTONE'S OWN PROGRESS — done ONLY. Same milestone, same three issues.
	prog, err := milestones.GetProgress(ctx, ms.ID, ws.ID)
	if err != nil {
		t.Fatalf("milestone progress: %v", err)
	}
	if prog.TotalIssues != 3 {
		t.Fatalf("milestone total = %d, want 3 — the two surfaces disagree on the DENOMINATOR too, "+
			"which is a different and larger defect than the one this test pins", prog.TotalIssues)
	}
	if prog.Completed != 1 {
		t.Errorf("milestone progress completed = %d, want 1 (done only)", prog.Completed)
	}

	// (3) THE CYCLE — done ONLY, measured rather than cited.
	cp, err := cycles.GetProgress(ctx, cyc.ID, ws.ID)
	if err != nil {
		t.Fatalf("cycle progress: %v", err)
	}
	if cp.TotalIssues != 3 {
		t.Fatalf("cycle total = %d, want 3", cp.TotalIssues)
	}
	if cp.Completed != 1 {
		t.Errorf("cycle progress completed = %d, want 1 (done only)", cp.Completed)
	}

	// (4) THE DIVERGENCE ITSELF, asserted as one statement rather than left to be inferred from
	// three separate expectations. This is the line that fails if anyone makes the two agree.
	if marker.CompletedCount == prog.Completed {
		t.Errorf("the roadmap and the milestone's own progress now agree at %d completed. "+
			"That is very likely GOOD — but it is a product decision this test was written to keep "+
			"visible, so record which answer won (and update cycle/store.go if it was left behind) "+
			"and then delete this assertion.", prog.Completed)
	}
	t.Logf("MEASURED over one fixture (1 done, 1 cancelled, 1 todo) attached to milestone %s and cycle %s:\n"+
		"  roadmap.go milestone rollup   completed_count = %d / %d   (done OR cancelled)\n"+
		"  milestone.GetProgress         completed       = %d / %d   (done only)\n"+
		"  cycle.GetProgress             completed       = %d / %d   (done only)",
		ms.ID, cyc.ID, marker.CompletedCount, marker.IssueCount, prog.Completed, prog.TotalIssues,
		cp.Completed, cp.TotalIssues)
}
