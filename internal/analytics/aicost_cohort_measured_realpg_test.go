package analytics_test

// aicost_cohort_measured_realpg_test.go — THE AI-COST REPORT IS THE SPEND THAT HAPPENED IN THE WINDOW.
//
// B18.33 (W3.17) took the decision this file used to pin: GetAICostTrends reads the ledger,
// ai_spend_events, by each charge's own created_at. Before it summed issues.ai_cost_usd (a lifetime
// total) over issues whose updated_at fell in the window, so an issue that spent $50.00 sixty days
// ago reported $0.00 in a 7-day window, and then a TITLE-ONLY EDIT — no AI call, no money — moved
// that window to $50.00 total and a $214.29 projected monthly. These tests assert that edit now moves
// nothing, and that spend inside the window is reported and agrees with the ledger.

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	tt "github.com/talyvor/track/internal/testutil"
)

const cohortWindowDays = 7

// seedSpentLongAgo builds the one fixture shape in which "touched in the window" and "spent in the
// window" give different answers: a single issue whose spend is OLD and whose last touch is under
// the caller's control.
func seedSpentLongAgo(t *testing.T, d *tt.DB, wsID, teamID string, cost float64) *model.Issue {
	t.Helper()
	ctx := context.Background()
	is := issue.NewStore(d.Pool)

	iss, err := is.Create(ctx, model.Issue{
		WorkspaceID: wsID, TeamID: teamID, Title: "spent long ago", CreatorID: "u1",
		Status: model.StatusTodo, LensFeature: "cohort-feat",
	})
	if err != nil {
		t.Fatalf("PREMISE FAILED: create: %v", err)
	}
	n, err := is.RecordSpendEvent(ctx, "cohort-evt", "cohort-feat", cost, 1000, wsID, "sync")
	if err != nil || n != 1 {
		t.Fatalf("PREMISE FAILED: RecordSpendEvent attributed %d issues (err=%v) — the fixture needs "+
			"the spend to land ON this issue or every figure below is about an empty cohort", n, err)
	}
	// The spend really did happen long ago: the LEDGER row and the issue's clock both move back.
	if _, err := d.Pool.Exec(ctx,
		`UPDATE ai_spend_events SET created_at = NOW() - INTERVAL '60 days' WHERE workspace_id = $1`,
		wsID); err != nil {
		t.Fatalf("PREMISE FAILED: backdate ledger: %v", err)
	}
	if _, err := d.Pool.Exec(ctx,
		`UPDATE issues SET updated_at = NOW() - INTERVAL '60 days',
                            created_at = NOW() - INTERVAL '60 days' WHERE id = $1`,
		iss.ID); err != nil {
		t.Fatalf("PREMISE FAILED: backdate issue: %v", err)
	}
	return iss
}

// ledgerSpentInWindow is what the report CLAIMS to be about, read from the table that can answer it.
func ledgerSpentInWindow(t *testing.T, d *tt.DB, wsID string, days int) float64 {
	t.Helper()
	var v float64
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(cost_usd), 0) FROM ai_spend_events
          WHERE workspace_id = $1 AND created_at > NOW() - (INTERVAL '1 day' * $2::int)`,
		wsID, days).Scan(&v); err != nil {
		t.Fatalf("ledger window: %v", err)
	}
	return v
}

func TestAICostReport_ATitleEditDoesNotMoveOldSpendIntoTheWindow_RealPG(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	eng := analytics.New(d.Pool)

	iss := seedSpentLongAgo(t, d, ws.ID, team.ID, 50.0)
	if got := ledgerSpentInWindow(t, d, ws.ID, cohortWindowDays); got != 0 {
		t.Fatalf("PREMISE FAILED: the ledger reports $%.2f spent in the last %d days, want $0.00", got, cohortWindowDays)
	}

	// A TITLE-ONLY EDIT. No AI call, no money, nothing bought.
	if _, err := issue.NewStore(d.Pool).Update(ctx, iss.ID, ws.ID,
		map[string]any{"title": "renamed today, no AI used"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	rep, err := eng.GetAICostTrends(ctx, ws.ID, cohortWindowDays)
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	if rep.TotalCostUSD != 0 || rep.ProjectedMonthly != 0 || len(rep.DailyCosts) != 0 || len(rep.TopCostIssues) != 0 {
		t.Errorf("after a title-only edit the %d-day report shows total $%.2f, projected $%.2f, %d daily "+
			"bucket(s), %d leaderboard row(s); want all zero/empty — the $50.00 was spent 60 days ago",
			cohortWindowDays, rep.TotalCostUSD, rep.ProjectedMonthly, len(rep.DailyCosts), len(rep.TopCostIssues))
	}

	// The same report over 90 days includes it, on the day it was charged.
	rep, err = eng.GetAICostTrends(ctx, ws.ID, 90)
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	if rep.TotalCostUSD != 50 || len(rep.DailyCosts) != 1 {
		t.Fatalf("90-day report: total $%.2f over %d bucket(s), want $50.00 in 1", rep.TotalCostUSD, len(rep.DailyCosts))
	}
	wantDay := time.Now().UTC().AddDate(0, 0, -60).Truncate(24 * time.Hour)
	if got := rep.DailyCosts[0].Date.UTC().Truncate(24 * time.Hour); !got.Equal(wantDay) {
		t.Errorf("the $50.00 is bucketed on %s, want the day it was charged, %s", got, wantDay)
	}
}

// Spend inside the window is reported, and the report and the ledger agree.
func TestAICostReport_SpendInsideTheWindowIsReportedAndAgreesWithTheLedger_RealPG(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	is := issue.NewStore(d.Pool)
	eng := analytics.New(d.Pool)

	if _, err := is.Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, Title: "spent today", CreatorID: "u1",
		Status: model.StatusTodo, LensFeature: "today-feat",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if n, err := is.RecordSpendEvent(ctx, "today-evt", "today-feat", 12.5, 500, ws.ID, "sync"); err != nil || n != 1 {
		t.Fatalf("PREMISE FAILED: RecordSpendEvent attributed %d issues (err=%v)", n, err)
	}

	rep, err := eng.GetAICostTrends(ctx, ws.ID, cohortWindowDays)
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	if rep.TotalCostUSD != 12.5 {
		t.Errorf("total = $%.2f, want $12.50 — spend inside the window must be reported", rep.TotalCostUSD)
	}
	if got := ledgerSpentInWindow(t, d, ws.ID, cohortWindowDays); got != 12.5 {
		t.Errorf("ledger window = $%.2f, want $12.50", got)
	}
	if rep.AvgCostPerIssue != 12.5 {
		t.Errorf("avg = $%.2f, want $12.50 — one issue cost something", rep.AvgCostPerIssue)
	}
}
