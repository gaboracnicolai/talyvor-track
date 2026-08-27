package analytics_test

// aicost_cohort_measured_realpg_test.go — WHICH ISSUES THE AI-COST REPORT IS ABOUT.
//
// engine.go says it, one comment above the leaderboard, and says it is not a session's to change:
//
//	"ai_cost_usd is a LIFETIME running total per issue and updated_at is the row's LAST TOUCH, so
//	 every figure here is still 'the lifetime cost of issues touched in the window' rather than
//	 'the spend in the window'. ai_spend_events carries each event's own created_at and an index on
//	 (workspace_id, created_at DESC) for exactly that question, and no query in this repo reads it.
//	 That is a decision about what total_cost_usd means, written up with its numbers rather than
//	 taken here."
//
// That is right, and this file does not take the decision either. What it does is make the shipped
// cohort CHECKABLE, because it was not.
//
// ⚠ MEASURED AT `a12e01f`: change the totals query's window column from `updated_at` to
// `created_at` — which silently moves total_cost_usd, avg_cost_per_issue and projected_monthly_usd
// from "issues TOUCHED in the window" to "issues CREATED in the window" — and the WHOLE REPOSITORY
// STAYS GREEN, `go test -race ./...` exit 0. The cohort the comment calls a decision could be
// changed by anyone, in either direction, with nothing to say so. `mcp.Server.toolGetAICosts`
// publishes these numbers to an AGENT as "total spend … and projected monthly spend".
//
// ⚠⚠ AND THE CONSEQUENCE IS SHARPER THAN THE COMMENT'S WORDING, MEASURED HERE RATHER THAN
// REASONED: an issue that spent $50.00 sixty days ago reports $0.00 in a 7-day window — correctly,
// under any reading — and then a TITLE-ONLY EDIT, with no AI call and no money spent, moves the same
// 7-day window to $50.00 total and a $214.29 PROJECTED MONTHLY. Nothing was bought. The ledger for
// those seven days says $0.00 and is not consulted.
//
// SO THE PINS BELOW RECORD WHICH COHORT IS SHIPPED, NOT WHICH IS RIGHT. Each one REDS the day the
// decision is taken either way, which is the point: a decision about a customer-visible money
// figure should not be landable as a one-word edit. If you are reading this because a pin went red
// after you switched the report to ai_spend_events — that is the pin working. Delete it and write
// the windowed-spend assertion in its place, and say so in the queue.

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

func TestAICostReport_TheCohortIsTouchedInTheWindow_NotSpentInTheWindow_RealPG(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	eng := analytics.New(d.Pool)

	iss := seedSpentLongAgo(t, d, ws.ID, team.ID, 50.0)

	// [A-PREMISE-LEDGER] the ledger agrees the money is OLD. Without this every figure below could
	// be measuring a workspace that never spent anything, and the pins would pass on nothing.
	if got := ledgerSpentInWindow(t, d, ws.ID, cohortWindowDays); got != 0 {
		t.Fatalf("[A-PREMISE-LEDGER] the ledger reports $%.2f spent in the last %d days, want $0.00 — "+
			"the fixture's whole point is that the spend is outside the window", got, cohortWindowDays)
	}

	// [A-COHORT-UNTOUCHED] before anybody touches it, the report and the ledger agree.
	before, err := eng.GetAICostTrends(ctx, ws.ID, cohortWindowDays)
	if err != nil {
		t.Fatalf("trends: %v", err)
	}
	if before.TotalCostUSD != 0 {
		t.Errorf("[A-COHORT-UNTOUCHED] a %d-day report over an issue last touched 60 days ago totals "+
			"$%.2f, want $0.00 — the shipped cohort is `updated_at > NOW() - days`, so an untouched "+
			"issue is outside it", cohortWindowDays, before.TotalCostUSD)
	}

	// A TITLE-ONLY EDIT. No AI call, no money, nothing bought.
	if _, err := issue.NewStore(d.Pool).Update(ctx, iss.ID, ws.ID,
		map[string]any{"title": "renamed today, no AI used"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	after, err := eng.GetAICostTrends(ctx, ws.ID, cohortWindowDays)
	if err != nil {
		t.Fatalf("trends: %v", err)
	}

	// [A-COHORT-WINDOW-COLUMN] THE PIN. The window column is `updated_at`, so a rename brings the
	// issue's LIFETIME cost into the window. Changing that column to `created_at` — or to anything
	// else — reds here and nowhere else in the repository (measured at a12e01f).
	if after.TotalCostUSD != 50.0 {
		t.Errorf("[A-COHORT-WINDOW-COLUMN] after a TITLE-ONLY edit the %d-day total is $%.2f, want "+
			"$50.00.\n"+
			"  This pin records WHICH COHORT IS SHIPPED, not which is right: the window column is "+
			"`issues.updated_at` and `ai_cost_usd` is a lifetime running total, so a rename brings a "+
			"60-day-old $50.00 spend into a 7-day report.\n"+
			"  If this went red because you moved the report onto ai_spend_events.created_at, that is "+
			"this pin working — delete it, write the windowed-spend assertion, and say so in the queue.",
			cohortWindowDays, after.TotalCostUSD)
	}

	// [A-COHORT-LEDGER-DISAGREES] the same instant, from the table that can answer the question the
	// report's own field name asks. Pinned so the gap is a fact in CI rather than a comment.
	if got := ledgerSpentInWindow(t, d, ws.ID, cohortWindowDays); got != 0 {
		t.Errorf("[A-COHORT-LEDGER-DISAGREES] ledger window = $%.2f, want $0.00", got)
	} else if after.TotalCostUSD == 0 {
		t.Errorf("[A-COHORT-LEDGER-DISAGREES] the report and the ledger now AGREE at $0.00. That is " +
			"the corrected behaviour, not the shipped one — see the block at the top of this file")
	}

	// [A-COHORT-PROJECTION] the projection is extrapolated from that same figure, so a rename
	// invents a monthly run-rate on a workspace that bought nothing in the window.
	if after.ProjectedMonthly <= 0 {
		t.Errorf("[A-COHORT-PROJECTION] projected_monthly_usd = $%.2f, want > 0 — it is "+
			"(total/days)*30 over the touched-in-window cohort, and `mcp.Server.toolGetAICosts` "+
			"publishes it to an agent as \"projected monthly spend\"", after.ProjectedMonthly)
	}

	// [A-COHORT-DAILY-BUCKET] the daily series keys on the same column, so an issue's ENTIRE
	// lifetime cost lands on the single day it was last touched — today, not the day it was spent.
	if len(after.DailyCosts) != 1 {
		t.Errorf("[A-COHORT-DAILY-BUCKET] the daily series has %d buckets, want 1 — one issue has one "+
			"updated_at, so it contributes to exactly one day", len(after.DailyCosts))
	} else {
		b := after.DailyCosts[0]
		if b.CostUSD != 50.0 {
			t.Errorf("[A-COHORT-DAILY-BUCKET] the single bucket holds $%.2f, want $50.00 — the whole "+
				"lifetime cost attributed to the day of the rename", b.CostUSD)
		}
		// ⚠ THE DATE IS ASSERTED SEPARATELY, AND A CONTROL IS WHAT SAID IT HAD TO BE. The first
		// draft pinned only the bucket COUNT and AMOUNT, and control C2 — the daily series re-keyed
		// on created_at — went NOT CAUGHT by these pins: the row is still in the cohort (its
		// updated_at is today) and still worth $50, so one bucket holding $50 is true under both
		// keys. Only the DATE distinguishes them, and the date is the whole claim this tag makes.
		wantDay := time.Now().UTC().Format("2006-01-02")
		if got := b.Date.UTC().Format("2006-01-02"); got != wantDay {
			t.Errorf("[A-COHORT-DAILY-BUCKET] the bucket is dated %s, want %s — the series keys on "+
				"`updated_at`, so a 60-day-old spend is charted on the day of the RENAME. A bucket "+
				"dated 60 days ago would mean the series had been re-keyed on created_at while the "+
				"cohort still selects on updated_at, which is neither reading", got, wantDay)
		}
	}
}

// MUST STAY GREEN, AND IT IS WHAT KEEPS THE PINS ABOVE FROM BEING A CATCH-ALL: a workspace whose
// spend really did happen inside the window reports it, and the report and the ledger agree. A
// "cohort" pin that fired on every fixture would be measuring nothing.
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
