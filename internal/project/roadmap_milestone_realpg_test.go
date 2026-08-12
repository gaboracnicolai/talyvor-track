package project_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

// roadmap_milestone_realpg_test.go — THE ROADMAP RENDERS TWO NUMBERS PER MILESTONE AND NOTHING IN
// THIS REPO COULD MAKE EITHER OF THEM NON-ZERO.
//
// `MilestoneMarker.tsx` draws `${completed_count}/${issue_count} done` and, gated on `> 0`,
// `$${ai_cost_usd} AI` for every milestone on the Roadmap page (a sidebar route and a command-palette
// entry). All three come from roadmap.go's `LEFT JOIN issues i ON i.milestone_id = m.id`, and
// `issues.milestone_id` — declared by migration 0004, indexed by it, and also the key
// milestone.Store.GetProgress counts on — was named by NO write path: not by Create's INSERT, not by
// UpsertByIdentifier's, and not by `updatableFields`, so a PATCH carrying it was dropped by the
// allowlist and answered 200.
//
// THE CONTROL IS IN THE SAME QUERY AND ON THE SAME PAGE. The project rollup above the milestone one
// joins `i.project_id = p.id` — a column that IS writable — so one issue, one roadmap call, two
// numbers: the project says 1 and the milestone says 0. That is what makes this a measurement of the
// milestone join rather than of the fixture.
func TestRoadmap_AnIssueAttachedToAMilestoneIsCountedByThatMilestone_RealPG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.New(t)

	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	issues := issue.NewStore(db.Pool)
	projects := project.NewStore(db.Pool)
	milestones := milestone.NewStore(db.Pool)

	now := time.Now().UTC()
	target := now.Add(30 * 24 * time.Hour)
	proj, err := projects.Create(ctx, model.Project{
		WorkspaceID: ws.ID, TeamID: tm.ID, Name: "Ship it", Identifier: "SHIP",
		StartDate: &now, TargetDate: &target,
	})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	attached, err := milestones.Create(ctx, milestone.Milestone{
		WorkspaceID: ws.ID, ProjectID: proj.ID, Name: "Beta", TargetDate: &target,
	})
	if err != nil {
		t.Fatalf("seed milestone: %v", err)
	}
	// The empty milestone is the other half of the control: after the fix the attached one must
	// report 1 and this one must still report 0, so a green cannot come from counting every issue.
	empty, err := milestones.Create(ctx, milestone.Milestone{
		WorkspaceID: ws.ID, ProjectID: proj.ID, Name: "GA", TargetDate: &target,
	})
	if err != nil {
		t.Fatalf("seed empty milestone: %v", err)
	}

	iss, err := issues.Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: tm.ID, ProjectID: &proj.ID,
		Title: "the one issue", CreatorID: "seed", Status: model.StatusDone,
		LensFeature: "roadmap-milestone-fixture",
	})
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	// ⚠ THE SPEND GOES IN THROUGH THE SHIPPED RECORDER, NOT THE CREATE BODY, AND THE FIRST DRAFT OF
	// THIS TEST LEARNED THAT THE HARD WAY: `Create` does not name ai_cost_usd in its INSERT (the
	// column is reconciled in by RecordSpendEvent, keyed on lens_feature), so a fixture that sets
	// the struct field lands 0 and the AI-cost assertion below fails for a reason that has nothing
	// to do with the milestone join.
	if n, err := issues.RecordSpendEvent(ctx, "evt-roadmap-milestone", "roadmap-milestone-fixture",
		1.25, 10, ws.ID, "test"); err != nil || n != 1 {
		t.Fatalf("seed AI spend: matched %d issue(s), err=%v — want exactly 1", n, err)
	}

	// THE SHIPPED WRITE PATH, the one PATCH /v1/issues/{id} reaches — a field map, exactly as
	// issue.Handler.Update decodes it.
	if _, err := issues.Update(ctx, iss.ID, ws.ID, map[string]any{"milestone_id": attached.ID}); err != nil {
		t.Fatalf("attach issue to milestone: %v", err)
	}

	// (1) THE COLUMN. Read straight from the table, so this assertion cannot be satisfied by an
	// echo the store made up.
	var stored *string
	if err := db.Pool.QueryRow(ctx, `SELECT milestone_id FROM issues WHERE id = $1`, iss.ID).Scan(&stored); err != nil {
		t.Fatalf("read back milestone_id: %v", err)
	}
	if stored == nil || *stored != attached.ID {
		t.Errorf("issues.milestone_id = %v, want %q — the attach was accepted and never stored",
			derefOrNil(stored), attached.ID)
	}

	// (2) THE RENDERED NUMBERS.
	roadmap, err := projects.GetRoadmap(ctx, ws.ID, nil, now.Add(-24*time.Hour), target.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("roadmap: %v", err)
	}
	var rp *project.RoadmapProject
	for i := range roadmap {
		if roadmap[i].ID == proj.ID {
			rp = &roadmap[i]
		}
	}
	if rp == nil {
		t.Fatalf("roadmap returned no row for the seeded project (%d projects)", len(roadmap))
	}
	// THE CONTROL: the project rollup joins project_id, a writable column. If this is not 1 the
	// fixture never landed an issue and every milestone assertion below would be vacuous.
	if rp.IssueCount != 1 || rp.CompletedCount != 1 {
		t.Fatalf("control failed: project rollup says %d issues / %d completed, want 1/1 — "+
			"the fixture, not the milestone join, is what this test would be measuring",
			rp.IssueCount, rp.CompletedCount)
	}

	byName := map[string]project.RoadmapMilestone{}
	for _, m := range rp.Milestones {
		byName[m.Name] = m
	}
	got, ok := byName[attached.Name]
	if !ok {
		t.Fatalf("roadmap carries no milestone %q (has %d)", attached.Name, len(rp.Milestones))
	}
	if got.IssueCount != 1 {
		t.Errorf("milestone %q issue_count = %d, want 1 — the Roadmap page renders this as "+
			"\"%d/%d done\"", attached.Name, got.IssueCount, got.CompletedCount, got.IssueCount)
	}
	if got.CompletedCount != 1 {
		t.Errorf("milestone %q completed_count = %d, want 1", attached.Name, got.CompletedCount)
	}
	if got.AICostUSD != 1.25 {
		t.Errorf("milestone %q ai_cost_usd = %v, want 1.25 — MilestoneMarker.tsx renders this "+
			"only when it is > 0", attached.Name, got.AICostUSD)
	}
	// The other half of the control: attaching one issue must not light up every milestone.
	if e := byName[empty.Name]; e.IssueCount != 0 || e.AICostUSD != 0 {
		t.Errorf("unattached milestone %q reports %d issue(s) / $%v — the join is counting rows "+
			"that are not attached to it", empty.Name, e.IssueCount, e.AICostUSD)
	}

	// (3) THE OTHER SURFACE ON THE SAME COLUMN. milestone.GetProgress documents itself as
	// "computed live from the issues table"; it counts the same key.
	prog, err := milestones.GetProgress(ctx, attached.ID, ws.ID)
	if err != nil {
		t.Fatalf("milestone progress: %v", err)
	}
	if prog.TotalIssues != 1 || prog.Completed != 1 || prog.CompletionPct != 1 {
		t.Errorf("GetProgress = %d/%d (%.2f), want 1/1 (1.00)",
			prog.Completed, prog.TotalIssues, prog.CompletionPct)
	}
	// Its control, for the same reason the empty milestone above is one: a GetProgress that lost its
	// WHERE clause would count the workspace's only issue and answer 1/1 here too.
	emptyProg, err := milestones.GetProgress(ctx, empty.ID, ws.ID)
	if err != nil {
		t.Fatalf("empty milestone progress: %v", err)
	}
	if emptyProg.TotalIssues != 0 {
		t.Errorf("GetProgress for the unattached milestone = %d issues, want 0 — it is counting "+
			"rows that are not attached to it", emptyProg.TotalIssues)
	}
}

func derefOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
