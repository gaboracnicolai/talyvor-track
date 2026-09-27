package project_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

// The Roadmap forecasts each project from its own finished work. "Steady" was imported ten weeks
// ago and has finished two issues in each of those ten weeks, with six still open (and one
// cancelled, which is neither open nor finished) — so at its pace it finishes in three weeks.
// "Fresh", in the same workspace, has open work and nothing finished: it must say so rather than
// borrow Steady's pace.
func TestRoadmap_AProjectForecastsFromItsOwnFinishedWork_RealPG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.New(t)

	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	issues := issue.NewStore(db.Pool)
	projects := project.NewStore(db.Pool)

	now := time.Now().UTC()
	target := now.AddDate(0, 3, 0)
	steady, err := projects.Create(ctx, model.Project{
		WorkspaceID: ws.ID, TeamID: tm.ID, Name: "Steady", Identifier: "STD",
		StartDate: &now, TargetDate: &target,
	})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	fresh, err := projects.Create(ctx, model.Project{
		WorkspaceID: ws.ID, TeamID: tm.ID, Name: "Fresh", Identifier: "FRS",
		StartDate: &now, TargetDate: &target,
	})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Imported history keeps its dates: the oldest issue was created just under ten weeks ago.
	historyStart := now.Add(-70*24*time.Hour + time.Hour)
	seed := func(projectID string, status model.IssueStatus, completedAt *time.Time) {
		t.Helper()
		if _, err := issues.Create(ctx, model.Issue{
			WorkspaceID: ws.ID, TeamID: tm.ID, ProjectID: &projectID,
			Title: "work", CreatorID: model.ImporterCreatorID, Status: status,
			CreatedAt: historyStart, CompletedAt: completedAt,
		}); err != nil {
			t.Fatalf("seed issue: %v", err)
		}
	}
	for w := 0; w < 10; w++ {
		for range 2 {
			done := now.Add(-time.Duration(w*7*24+84) * time.Hour)
			seed(steady.ID, model.StatusDone, &done)
		}
	}
	for range 6 {
		seed(steady.ID, model.StatusTodo, nil)
	}
	seed(steady.ID, model.StatusCancelled, nil)
	for range 3 {
		seed(fresh.ID, model.StatusTodo, nil)
	}

	rows, err := projects.GetRoadmap(ctx, ws.ID, nil, now.AddDate(0, -1, 0), target)
	if err != nil {
		t.Fatalf("roadmap: %v", err)
	}
	byName := map[string]project.RoadmapProject{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	s := byName["Steady"].Forecast
	if s == nil || s.Status != "forecast" || s.Remaining != 6 || s.HistoryWeeks != 10 || s.FinishedInHistory != 20 {
		t.Fatalf("Steady forecast = %+v, want forecast, 6 open, 10 weeks, 20 finished", s)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	want := today.AddDate(0, 0, 21)
	if s.Likely == nil || !s.Likely.Equal(want) || s.Safe == nil || !s.Safe.Equal(want) {
		t.Fatalf("Steady likely=%v safe=%v, want both %v (two a week, six open)", s.Likely, s.Safe, want)
	}

	f := byName["Fresh"].Forecast
	if f == nil || f.Status != "no_history" || f.Remaining != 3 || f.Likely != nil {
		t.Fatalf("Fresh forecast = %+v, want no_history with 3 open and no date", f)
	}
}
