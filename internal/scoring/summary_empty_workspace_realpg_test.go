package scoring_test

// THE SCORING SUMMARY'S `top_issue_id` PUTS ITS `COALESCE` INSIDE A NON-AGGREGATE SCALAR SUBQUERY,
// WHERE IT IS DEAD ON THE ROW IT CAN SEE AND ABSENT ON THE POPULATION IT WAS WRITTEN FOR.
//
// `Store.GetScoreSummary` is one statement of five scalar subqueries scanned into five Go values.
// Four of them are aggregates — COUNT, COUNT, AVG, AVG — and an aggregate over an empty table still
// returns EXACTLY ONE ROW, so `COALESCE(AVG(rice_score), 0)` genuinely converts the empty case to 0.
// The fifth is not an aggregate:
//
//	(SELECT COALESCE(issue_id, '') FROM issue_scores WHERE workspace_id = $1
//	     ORDER BY GREATEST(...) DESC LIMIT 1) AS top_issue_id
//
// A scalar subquery that selects ZERO ROWS evaluates to NULL, and a COALESCE written INSIDE it never
// runs, because there is no row to run it on. The four lines sit adjacent and read identically.
//
// ⚠ AND THE COALESCE IS DEAD IN THE OTHER DIRECTION TOO, WHICH IS WHY THIS IS NOT A TYPO BUT A
// MISPLACED GUARD: `issue_scores.issue_id` is `TEXT UNIQUE NOT NULL` (migration 0015 line 11), so on
// any row that DOES exist the argument can never be NULL. The guard is vacuous where it is written
// and missing where it is needed — the same guard, failing in both directions at once.
//
// ⚠ WHY NOTHING CAUGHT IT: every test in `internal/scoring` runs on `pgxmock`. The one that names
// this function, `TestGetScoreSummary_CalculatesCoverage`, constructs the result row itself and puts
// the literal string `"i-top"` in the `top_issue_id` column. A mock cannot return "zero rows from a
// scalar subquery" for a statement it never plans, so the population that exposes this — a workspace
// that has not been scored yet, which is EVERY workspace on the day it is created — had never
// reached a real planner.
//
// MEASURED, NOT READ, on pgvector/pgvector:pg16 from zero through the production migration runner.
// The destination is a plain Go `string`, so the NULL is a scan error, and `Handler.Summary` turns a
// non-nil error into HTTP 500 `SUMMARY_FAILED` — the empty state of a shipped endpoint.
//
// ⚠ THE SECOND TEST IS THE MUST-STAY-GREEN CONTROL and it is doing real work: it is what stops a
// "fix" that simply hard-codes `''` from passing, and it is the arm that proves this file can still
// tell a populated summary's real numbers apart from a default-shaped one.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/scoring"
	"github.com/talyvor/track/internal/testutil"
)

// seedScore writes one issue_scores row directly. It bypasses SetScore deliberately: SetScore
// validates and recomputes, and the question here is what the SUMMARY statement does with rows that
// are already in the table, not how they got there.
func seedScore(t *testing.T, d *testutil.DB, issueID, wsID, method string, rice, ice *float64) {
	t.Helper()
	_, err := d.Pool.Exec(context.Background(),
		`INSERT INTO issue_scores (issue_id, workspace_id, method, rice_score, ice_score)
		 VALUES ($1, $2, $3, $4, $5)`,
		issueID, wsID, method, rice, ice)
	if err != nil {
		t.Fatalf("seed issue_scores for %s: %v", issueID, err)
	}
}

func f(v float64) *float64 { return &v }

// TestMeasured_ScoreSummary_OnAWorkspaceThatHasNeverBeenScored is the red.
//
// The workspace is real and populated with ISSUES — it is not an empty tenant — it simply has no
// rows in `issue_scores`, which is the state of every workspace between creation and the first time
// somebody scores something. Coverage is legitimately 0%, and that is a NUMBER the summary is
// supposed to be able to report.
func TestMeasured_ScoreSummary_OnAWorkspaceThatHasNeverBeenScored(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := scoring.NewStore(d.Pool)

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	for i := 0; i < 3; i++ {
		d.Issue(t, ws.ID, team.ID)
	}

	out, err := store.GetScoreSummary(ctx, ws.ID)
	if err != nil {
		t.Fatalf("GetScoreSummary on an unscored workspace returned an error, so the endpoint's "+
			"empty state is an HTTP 500: %v\n"+
			"If this is now green because top_issue_id was moved OUT of the subquery, that is the "+
			"fix landing — keep this test, do not delete it.", err)
	}
	if out.TotalIssues != 3 {
		t.Errorf("total_issues = %d, want 3 — the issues exist even though no score does", out.TotalIssues)
	}
	if out.TotalScored != 0 {
		t.Errorf("total_scored = %d, want 0", out.TotalScored)
	}
	if out.CoverageRate != 0 {
		t.Errorf("coverage_pct = %v, want 0", out.CoverageRate)
	}
	if out.AvgRICE != 0 || out.AvgICE != 0 {
		t.Errorf("avg rice/ice = %v/%v, want 0/0 — these two ARE guarded, because AVG is an aggregate",
			out.AvgRICE, out.AvgICE)
	}
	if out.TopIssueID != "" {
		t.Errorf("top_issue_id = %q, want \"\" — there is no top issue when nothing is scored", out.TopIssueID)
	}
}

// TestMeasured_ScoreSummary_OnAPopulatedWorkspace is the must-stay-green control: it passes on main
// TODAY and must still pass after the fix. Every expected value is distinct so no single wrong term
// can land on another's number by luck.
func TestMeasured_ScoreSummary_OnAPopulatedWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := scoring.NewStore(d.Pool)

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	// Four issues, two of them scored → coverage 50%.
	i1 := d.Issue(t, ws.ID, team.ID)
	i2 := d.Issue(t, ws.ID, team.ID)
	d.Issue(t, ws.ID, team.ID)
	d.Issue(t, ws.ID, team.ID)

	seedScore(t, d, i1.ID, ws.ID, "rice", f(10), nil)
	seedScore(t, d, i2.ID, ws.ID, "rice", f(30), nil)

	// A SECOND workspace, scored HIGHER, so a lost workspace scope shows up as the wrong top issue
	// rather than as nothing at all.
	other := d.Workspace(t)
	otherTeam := d.Team(t, other.ID)
	oi := d.Issue(t, other.ID, otherTeam.ID)
	seedScore(t, d, oi.ID, other.ID, "rice", f(999), nil)

	out, err := store.GetScoreSummary(ctx, ws.ID)
	if err != nil {
		t.Fatalf("GetScoreSummary on a populated workspace: %v", err)
	}
	if out.TotalIssues != 4 {
		t.Errorf("total_issues = %d, want 4", out.TotalIssues)
	}
	if out.TotalScored != 2 {
		t.Errorf("total_scored = %d, want 2", out.TotalScored)
	}
	if out.CoverageRate < 49.9 || out.CoverageRate > 50.1 {
		t.Errorf("coverage_pct = %v, want 50", out.CoverageRate)
	}
	if out.AvgRICE < 19.9 || out.AvgRICE > 20.1 {
		t.Errorf("avg_rice = %v, want 20 — the mean of 10 and 30, and NOT of 999", out.AvgRICE)
	}
	if out.TopIssueID != i2.ID {
		t.Errorf("top_issue_id = %q, want %q (the 30, not the other workspace's 999)", out.TopIssueID, i2.ID)
	}
}
