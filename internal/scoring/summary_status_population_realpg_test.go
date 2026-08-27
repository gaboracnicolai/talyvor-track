package scoring_test

// THE SUMMARY'S TWO COVERAGE NUMBERS ARE COUNTED FROM DIFFERENT POPULATIONS, AND THE PERCENTAGE
// THEY PRODUCE CAN EXCEED 100.
//
// `GetScoreSummary` reads them from two scalar subqueries side by side:
//
//	(SELECT COUNT(*) FROM issues       WHERE workspace_id = $1 AND status != 'cancelled') AS total_issues
//	(SELECT COUNT(*) FROM issue_scores WHERE workspace_id = $1)                           AS total_scored
//
// The first excludes cancelled issues. The second filters nothing but the workspace. Cancelling a
// scored issue leaves its `issue_scores` row exactly where it was — there is no cascade on status
// and nothing deletes the score — so the denominator drops while the numerator does not, and
// `coverage_pct = total_scored / total_issues * 100` walks past 100.
//
// ⚠ WHICH POPULATION IS "RIGHT" IS A QUESTION; THAT THE TWO MUST AGREE IS NOT, AND THAT IS WHAT
// THIS FILE PINS. A ratio whose numerator and denominator are drawn from different sets is wrong
// whichever set you prefer, so the assertion is the INVARIANT — total_scored ≤ total_issues, and
// therefore coverage ≤ 100 — rather than a preferred definition. If a later session decides
// cancelled issues SHOULD be in the denominator, this file reds and says so instead of being
// quietly satisfied by the other repair.
//
// ⚠ TWO SESSIONS ALREADY WORKED THIS EXACT STATEMENT AND NEITHER TOUCHED THE STATUS POPULATION.
// W3.9 (`f54370c`) fixed the NULL scan that 500'd every never-scored workspace; W3.10 (`0f157c0`)
// guarded the WORKSPACE scope of these same predicates and pinned its own limits. The workspace
// half is watched from both directions. The status half was watched by nothing — measured, not
// assumed: with this file absent the whole repository suite is green on a workspace reporting 200%.
//
// ⚠ AND THE EXISTING POPULATED TEST CANNOT SEE IT, BY CONSTRUCTION. Its four issues are all
// live, so `status != 'cancelled'` and "no filter" select the same rows and the two subqueries
// agree for a reason that has nothing to do with either being right. A fixture in which every
// member satisfies both populations cannot tell them apart — the same shape as the billing
// fixtures that all stamped a non-empty APIKeyID, one repository over.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/scoring"
	"github.com/talyvor/track/internal/testutil"
)

func TestMeasured_ScoreSummary_CountsIssuesAndScoresFromTheSamePopulation(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := scoring.NewStore(d.Pool)

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// Three issues, ALL scored, TWO of them then cancelled — the ordinary life of tickets that
	// were prioritised and then dropped.
	//
	// ⚠ THE POPULATIONS ARE DELIBERATELY DIFFERENT SIZES, AND A CONTROL IS WHY. The first version
	// used one live and one cancelled: the right answer and the exactly-wrong answer are then BOTH
	// 1, so control S3 — which inverts the join so the numerator counts the CANCELLED scores
	// instead of the live ones — left every count assertion here green and was caught only by
	// pre-existing tests. **A count cannot tell two populations of the same size apart.** With 1
	// live and 2 cancelled, the inverted numerator is 2 against a denominator of 1 and the
	// invariant fires.
	live := d.Issue(t, ws.ID, team.ID)
	dropped := d.Issue(t, ws.ID, team.ID)
	alsoDropped := d.Issue(t, ws.ID, team.ID)
	seedScore(t, d, live.ID, ws.ID, "rice", f(10), nil)
	seedScore(t, d, dropped.ID, ws.ID, "rice", f(20), nil)
	seedScore(t, d, alsoDropped.ID, ws.ID, "rice", f(30), nil)

	if _, err := d.Pool.Exec(ctx,
		`UPDATE issues SET status = 'cancelled' WHERE id = ANY($1)`,
		[]string{dropped.ID, alsoDropped.ID}); err != nil {
		t.Fatalf("cancel the dropped issues: %v", err)
	}

	// ⚠ THE FIXTURE IS PROVEN LIVE BEFORE ANYTHING IS CONCLUDED FROM IT. If the score row went
	// away with the status change there would be no defect to measure, and this test would be
	// asserting an invariant that holds for a reason it does not state.
	var scoreRows int
	if err := d.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issue_scores WHERE workspace_id = $1`, ws.ID).Scan(&scoreRows); err != nil {
		t.Fatalf("count score rows: %v", err)
	}
	if scoreRows != 3 {
		t.Fatalf("issue_scores holds %d rows for this workspace, want 3. Cancelling an issue was "+
			"expected to leave its score behind — if something now removes it, this whole file is "+
			"measuring a property that no longer exists and should be re-derived, not adjusted.",
			scoreRows)
	}

	out, err := store.GetScoreSummary(ctx, ws.ID)
	if err != nil {
		t.Fatalf("GetScoreSummary: %v", err)
	}

	// THE INVARIANT. Not a preferred definition — an arithmetic one.
	if out.TotalScored > out.TotalIssues {
		t.Errorf("total_scored = %d and total_issues = %d: MORE ISSUES ARE SCORED THAN EXIST.\n"+
			"`total_issues` excludes cancelled issues and `total_scored` filters nothing but the "+
			"workspace, so cancelling a scored issue drops the denominator and leaves the "+
			"numerator. coverage_pct = %v.",
			out.TotalScored, out.TotalIssues, out.CoverageRate)
	}
	if out.CoverageRate > 100.0 {
		t.Errorf("coverage_pct = %v — a percentage of the workspace's issues that have been "+
			"scored, reported above 100. Whichever population is the right one, these two "+
			"numbers have to be counted from the same set.", out.CoverageRate)
	}

	// ⚠ MUST STAY GREEN: the live, scored issue is still counted. A repair that fixes the ratio by
	// emptying it is not a repair, and this is the arm that refuses that.
	if out.TotalIssues != 1 {
		t.Errorf("total_issues = %d, want 1 — the one issue that is not cancelled", out.TotalIssues)
	}
	if out.TotalScored != 1 {
		t.Errorf("total_scored = %d, want 1 — the score of the one issue still in the population",
			out.TotalScored)
	}
	if out.CoverageRate < 99.9 || out.CoverageRate > 100.1 {
		t.Errorf("coverage_pct = %v, want 100 — one live issue, and it is scored", out.CoverageRate)
	}

	// ⚠ THE THIRD SYMPTOM, AND THE ONE A READER ACTS ON. `top_issue_id` came from the same
	// unfiltered set: MEASURED before the repair, a live issue scored 10 and a CANCELLED one
	// scored 20 returned the CANCELLED id as the workspace's top-scoring issue. A prioritisation
	// surface pointing at dropped work is worse than a wrong count.
	//
	// ⚠ IT IS ALSO WHAT MAKES THE TWO COUNTS ABOVE DISTINGUISHABLE, WHICH A CONTROL HAD TO TEACH
	// THIS FILE. Control S3 inverted the join so the numerator counted the CANCELLED score
	// instead of the live one; total_scored is 1 either way, so every count assertion above stayed
	// green and only pre-existing tests caught it. An identity assertion tells the two apart.
	if out.TopIssueID != live.ID {
		t.Errorf("top_issue_id = %q, want the live issue %q (BOTH cancelled issues scored HIGHER, "+
			"at 20 and 30 against 10). The top-issue subquery must draw from the same population "+
			"as the counts beside it.", out.TopIssueID, live.ID)
	}

	// ⚠ PINNED, NOT CHANGED — AND STATED AS AN OPEN QUESTION RATHER THAN AN OVERSIGHT. The two
	// average subqueries still read every score row in the workspace, so this workspace's
	// avg_rice is 20 (the mean of the live 10 and the CANCELLED 20 and 30) rather than 10. Unlike a
	// coverage ratio above 100 or a cancelled top issue, that is not self-evidently wrong: an
	// average over all scoring effort is a defensible thing to report. It is only INCONSISTENT
	// with the three numbers beside it, and choosing which meaning the averages carry is a
	// product call, not a session's. It is asserted here so the current answer is written down
	// and cannot change without saying so.
	if out.AvgRICE < 19.9 || out.AvgRICE > 20.1 {
		t.Errorf("avg_rice = %v, want 20 — the mean of the live 10 and the CANCELLED 20 and 30. If this "+
			"is now 10 the averages have been moved onto the live population, which is a "+
			"deliberate answer to an open question and belongs in the queue entry, not here.",
			out.AvgRICE)
	}
}
