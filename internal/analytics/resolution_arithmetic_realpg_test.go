package analytics_test

// TWO OF THE FIVE NUMBERS THE RESOLUTION REPORT SERVES HAD NO ASSERTION ANYWHERE IN THE REPO, AND
// A COMMENT CLAIMING THE SAMPLE SIZE CANNOT DISAGREE WITH THE PERCENTILES WAS NEVER TESTED.
//
// analytics.GetTimeToResolution computes all five of its numbers IN SQL — an AVG and three
// PERCENTILE_CONTs over `EXTRACT(EPOCH FROM completed_at - created_at)/3600`, plus a COUNT(*), and
// then a SECOND query for the per-priority median. The only test that named it,
// engine_test.go's TestGetTimeToResolution_CalculatesMedianCorrectly, is a pgxmock test: it FEEDS
// the row `(7, 48.5, 24.0, 36.0, 96.0)` and asserts the struct carries those values back. It
// asserts that columns are scanned into the right fields, which is worth having and is not what
// its name claims.
//
// MEASURED, NOT INFERRED, at 882c94d — nine one-term mutations of the shipped SQL, each run over
// the WHOLE import closure (`./internal/analytics/ ./internal/importer/`), each restored in a
// `finally` and sha256-verified (scripts/w34-resolution-arithmetic-controls-8d3f.py):
//
//	M1  global AVG            /3600 -> /60        avg_hours served in MINUTES     NOT CAUGHT
//	M2  per-priority          p50 -> p95          the median becomes the tail     NOT CAUGHT
//	M6  global WHERE drops `completed_at IS NOT NULL`
//	                                              COUNT(*) counts rows the
//	                                              percentiles never saw          NOT CAUGHT
//	M4  global p95 column     0.95 -> 0.75        p95 serves the p75              caught — SHAPE only
//	M7  global p75 column     0.75 -> 0.5         p75 serves the median           caught — SHAPE only
//	M3  global p50            /3600 -> /60        median in minutes               caught (importer)
//	M5  per-priority team scope neutralised       another team's medians          caught
//
// RE-RUN WITH THIS FILE PRESENT, SAME SCRIPT, SAME COMMAND: all eight mutations are CAUGHT, and
// M1/M2/M6 — the three that were blind across the whole closure — are caught BY THIS FILE. M4 and
// M7 gain a VALUE catch beside the mock's text catch; M5 gains a third.
//
// ⚠ THE HARNESS GRADES ITSELF BEFORE ITS VERDICTS ARE WORTH READING, AND IT CAUGHT ME ONCE. The
// script carries a POS control that MUST red and a VOID control (`/3600 + 0`, arithmetically
// identity) that MUST NOT. VOID is still NOT CAUGHT after this file lands, which is the evidence
// that these assertions are keyed on the query's ANSWERS and not on its text. The first POS
// control I wrote — dropping `completed_at IS NOT NULL` — scored NOT CAUGHT and failed the
// self-check; the prediction was wrong, not the harness, and the reason it was wrong became M6.
//
// ⚠ ALSO NOTE WHY THIS HARNESS DOES NOT READ AN EXIT CODE. `./internal/importer/` already fails on
// a pristine tree on any machine holding empty /tmp/w34-jira-corpus and /tmp/w34-linear-corpus-cache
// dirs (13 FAIL names, correct fail-closed behaviour, LOCAL ONLY — CI has no such dirs, the
// censuses t.Skipf, and CI is green). A control scoring `rc != 0` as CAUGHT would have scored all
// eight mutations caught and reported this file as unnecessary.
//
// ⚠ THE HANDOVER THIS FILE ANSWERS SAID GetTimeToResolution HAS "NO BEHAVIOURAL ORACLE AT ALL".
// MEASURED, THAT IS TOO STRONG AND THE CORRECTION MATTERS: the global MEDIAN does have one —
// internal/importer/api_resolution_job_test.go:181 asserts it to 0.01h against a hand-computed
// figure, and M3 reds SEVEN importer tests. So does the team scope (M5). What had NO oracle is
// narrower and more specific: **AvgHours, P75Hours and P95Hours by value, SampleSize against real
// Postgres, and the per-priority map by value.** `AvgHours` and `P75Hours` appeared in ZERO
// assertions in the entire repository — only in the struct definition and the Scan.
//
// ⚠ M4 AND M7 ARE THE TRAP THIS FILE EXISTS FOR. Both ARE caught today — by
// TestGetTimeToResolution_CalculatesMedianCorrectly, whose pgxmock ExpectQuery regex names the
// literals `PERCENTILE_CONT\(0\.75\)` and `PERCENTILE_CONT\(0\.95\)`. That is a catch on the SQL's
// TEXT, not on its answers: it fires because the query stopped containing a string, and it would
// keep firing if the query contained that string and returned the wrong number. A percentile can
// still be wrong in every way that does not change those two literals. The assertions below are on
// the VALUES, so the two catches become independent rather than duplicated.
//
// ⚠ WHAT THIS FILE PINS AND DOES NOT ENDORSE:
//
//  1. THE WINDOW KEYS ON created_at, NOT completed_at. An issue created 400 days ago and finished
//     yesterday is outside a 365-day report of "time to resolution". engine.go:388-394 already
//     records this as an open product decision; this file seeds inside the window so it measures
//     arithmetic rather than re-litigating that choice.
//  2. THE PER-PRIORITY MAP IS KEYED BY `priority::text`, so it carries the raw integer — "1", "3" —
//     and no vocabulary. The suite renders these; that is a presentation decision made elsewhere.
//
// ⚠ EVERY EXPECTED NUMBER BELOW IS DERIVED IN THE COMMENT BESIDE IT FROM PERCENTILE_CONT's OWN
// DEFINITION (continuous percentile: 0-based position `fraction*(N-1)` into the sorted sample,
// linearly interpolated), NOT read off a passing run. A fixture whose expectations were harvested
// from the code under test cannot fail.

import (
	"context"
	"math"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// resolutionEpsilon is tight enough that every mutation in the census above moves a number further
// than this (the smallest real gap is p75 8.0 -> p50 4.0), and loose enough to absorb float64
// interpolation. It is NOT a tolerance for "roughly right".
const resolutionEpsilon = 1e-6

// seedResolutionIssue writes one issue whose resolution time is EXACTLY `hours`.
//
// created_at and completed_at are both computed in SQL from one NOW(), so the fixture and the
// query share a clock: a test that builds timestamps from the test process's wall clock and
// asserts them against Postgres's own is measuring skew as well as arithmetic. `hours < 0` means
// UNRESOLVED — completed_at stays NULL, which is the state the cohort rules are about.
func seedResolutionIssue(t *testing.T, d *testutil.DB, wsID, teamID string, n, priority int, hours float64) {
	t.Helper()
	// The NULL branch takes FOUR parameters, not five — an unused $5 is a "mismatched param and
	// argument count" from pgx, so the argument list is built alongside the SQL rather than
	// beside it.
	args := []any{wsID, teamID, n, priority}
	completed := "NULL"
	if hours >= 0 {
		// 10 days back is inside every window this file asks for, so no case is silently
		// answered by the window predicate instead of by the term under test.
		completed = "NOW() - INTERVAL '10 days' + ($5::float8 * INTERVAL '1 hour')"
		args = append(args, hours)
	}
	_, err := d.Pool.Exec(context.Background(), `
        INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
                            creator_id, created_at, completed_at)
        VALUES ($1, $2, $3::int, 'RES-' || $3::int, 'resolution ' || $3::int, 'done', $4::int,
                'resprobe', NOW() - INTERVAL '10 days', `+completed+`)`,
		args...)
	if err != nil {
		t.Fatalf("seed issue %d (priority %d, %.1fh): %v", n, priority, hours, err)
	}
}

func closeTo(got, want float64) bool { return math.Abs(got-want) <= resolutionEpsilon }

// TestGetTimeToResolution_TheSQLsOwnArithmetic_RealPG asserts all five global numbers and both
// per-priority medians by VALUE, through the shipped method, against real Postgres.
func TestGetTimeToResolution_TheSQLsOwnArithmetic_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// The cohort, in hours: priority 1 gets {1, 2, 100}, priority 3 gets {4, 8}.
	// Chosen so that NO two of the seven expected numbers below coincide — a fixture where the
	// median happens to equal the p75 cannot tell a p50 from a p75.
	seedResolutionIssue(t, d, ws.ID, team.ID, 1, 1, 1)
	seedResolutionIssue(t, d, ws.ID, team.ID, 2, 1, 2)
	seedResolutionIssue(t, d, ws.ID, team.ID, 3, 1, 100)
	seedResolutionIssue(t, d, ws.ID, team.ID, 4, 3, 4)
	seedResolutionIssue(t, d, ws.ID, team.ID, 5, 3, 8)

	stats, err := analytics.New(d.Pool).GetTimeToResolution(ctx, ws.ID, "", 365)
	if err != nil {
		t.Fatalf("GetTimeToResolution: %v", err)
	}

	// Sorted sample: [1, 2, 4, 8, 100], N = 5.
	for _, tc := range []struct {
		name string
		got  float64
		want float64
		why  string
	}{
		{"SampleSize", float64(stats.SampleSize), 5,
			"five rows carry a completed_at inside the window"},
		{"AvgHours", stats.AvgHours, 23,
			"(1+2+4+8+100)/5 = 23 HOURS. If this reads 1380 the SQL is dividing the epoch by 60 " +
				"and the field whose NAME is its unit is serving minutes"},
		{"MedianHours", stats.MedianHours, 4,
			"PERCENTILE_CONT(0.5): position 0.5*(5-1) = 2.0 -> the third value, 4"},
		{"P75Hours", stats.P75Hours, 8,
			"PERCENTILE_CONT(0.75): position 0.75*4 = 3.0 -> the fourth value, 8"},
		{"P95Hours", stats.P95Hours, 81.6,
			"PERCENTILE_CONT(0.95): position 0.95*4 = 3.8 -> 8 + 0.8*(100-8) = 81.6. This is the " +
				"only number in the report that the 100h outlier moves, which is what a p95 is for"},
	} {
		if !closeTo(tc.got, tc.want) {
			t.Errorf("%s = %v, want %v — %s", tc.name, tc.got, tc.want, tc.why)
		}
	}

	// The per-priority breakdown is a MEDIAN, by its own comment ("median only, keeps the surface
	// narrow"). Both buckets are shaped so the median and the p95 of the SAME rows differ, so a
	// query that asks Postgres for the wrong percentile cannot answer with the right number.
	if len(stats.ByPriority) != 2 {
		t.Errorf("ByPriority has %d keys (%v), want exactly 2 — the two priorities seeded",
			len(stats.ByPriority), stats.ByPriority)
	}
	if got, ok := stats.ByPriority["1"]; !ok || !closeTo(got, 2) {
		t.Errorf("ByPriority[\"1\"] = %v (present %v), want 2 — PERCENTILE_CONT(0.5) over "+
			"[1, 2, 100] is position 0.5*2 = 1.0, the middle value. The p95 of the same rows is "+
			"90.2, so this assertion separates a median from a tail", got, ok)
	}
	if got, ok := stats.ByPriority["3"]; !ok || !closeTo(got, 6) {
		t.Errorf("ByPriority[\"3\"] = %v (present %v), want 6 — PERCENTILE_CONT(0.5) over [4, 8] "+
			"is position 0.5, interpolated: 4 + 0.5*(8-4) = 6. The p95 of the same rows is 7.8", got, ok)
	}
}

// TestGetTimeToResolution_SampleSizeIsTheCohortThePercentilesUsed_RealPG is the assertion behind
// engine.go's own comment: "COUNT(*) rides the SAME WHERE clause as the aggregates ... no row it
// counts is a row the percentiles skipped."
//
// ⚠ THE REASON THIS NEEDS ITS OWN TEST IS THE REASON IT WAS MISSED. Dropping
// `completed_at IS NOT NULL` from the global query does NOT move the average or any percentile:
// `EXTRACT(EPOCH FROM NULL - created_at)` is NULL, and AVG and PERCENTILE_CONT both ignore NULLs.
// Only COUNT(*) moves. So the failure this file guards against is INVISIBLE in all four of the
// other numbers, and a suite that asserts them all and skips the count sees nothing.
func TestGetTimeToResolution_SampleSizeIsTheCohortThePercentilesUsed_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// Two resolved, inside the window: sample [2, 6].
	seedResolutionIssue(t, d, ws.ID, team.ID, 1, 1, 2)
	seedResolutionIssue(t, d, ws.ID, team.ID, 2, 1, 6)
	// Three UNRESOLVED, same workspace, same team, same window. Under the shipped WHERE these are
	// invisible to every one of the five numbers.
	seedResolutionIssue(t, d, ws.ID, team.ID, 3, 1, -1)
	seedResolutionIssue(t, d, ws.ID, team.ID, 4, 3, -1)
	seedResolutionIssue(t, d, ws.ID, team.ID, 5, 3, -1)

	stats, err := analytics.New(d.Pool).GetTimeToResolution(ctx, ws.ID, "", 365)
	if err != nil {
		t.Fatalf("GetTimeToResolution: %v", err)
	}

	if stats.SampleSize != 2 {
		t.Errorf("SampleSize = %d, want 2 — three issues in this workspace have no completed_at, "+
			"and the report's own comment says COUNT(*) rides the same WHERE clause as the "+
			"aggregates. At 5 the count is a SEPARATELY FILTERED census: it would be describing a "+
			"cohort of five to numbers computed over two", stats.SampleSize)
	}
	// Stated as assertions rather than as prose so the claim "the percentiles did not move" is
	// itself measured. Sample [2, 6]: avg 4, p50/p75/p95 all interpolate between 2 and 6.
	if !closeTo(stats.AvgHours, 4) {
		t.Errorf("AvgHours = %v, want 4 — (2+6)/2 over the RESOLVED rows only", stats.AvgHours)
	}
	if !closeTo(stats.MedianHours, 4) {
		t.Errorf("MedianHours = %v, want 4 — PERCENTILE_CONT(0.5) over [2, 6]", stats.MedianHours)
	}
	// Every seeded priority appears in the workspace, but only the RESOLVED priority may appear in
	// the breakdown: the second query carries the same completed_at predicate.
	if len(stats.ByPriority) != 1 {
		t.Errorf("ByPriority = %v, want exactly the one priority with resolved work. Priority 3 "+
			"has only unresolved issues, so a bucket for it is a bucket with no measured rows",
			stats.ByPriority)
	}
}

// TestGetTimeToResolution_TeamScopeReachesTheSecondQuery_RealPG pins the team filter on the
// PER-PRIORITY query specifically. The two queries build their scope from one shared `teamSQL`
// string, so a scope that is right in the first and absent in the second serves one team's headline
// numbers beside every team's breakdown — and the breakdown is the half nothing read.
func TestGetTimeToResolution_TeamScopeReachesTheSecondQuery_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	mine := d.Team(t, ws.ID)
	other := d.Team(t, ws.ID)

	// MY team: priority 1 -> {1, 2, 100} (median 2), priority 3 -> {4, 8} (median 6).
	seedResolutionIssue(t, d, ws.ID, mine.ID, 1, 1, 1)
	seedResolutionIssue(t, d, ws.ID, mine.ID, 2, 1, 2)
	seedResolutionIssue(t, d, ws.ID, mine.ID, 3, 1, 100)
	seedResolutionIssue(t, d, ws.ID, mine.ID, 4, 3, 4)
	seedResolutionIssue(t, d, ws.ID, mine.ID, 5, 3, 8)
	// THE OTHER team, same workspace. Priority 1 at 50h twice would drag my priority-1 median from
	// 2 to 50 if the second query lost its scope; priority 7 exists ONLY here, so an unscoped
	// breakdown grows a key that my team has no work in at all.
	seedResolutionIssue(t, d, ws.ID, other.ID, 6, 1, 50)
	seedResolutionIssue(t, d, ws.ID, other.ID, 7, 1, 50)
	seedResolutionIssue(t, d, ws.ID, other.ID, 8, 7, 9)

	stats, err := analytics.New(d.Pool).GetTimeToResolution(ctx, ws.ID, mine.ID, 365)
	if err != nil {
		t.Fatalf("GetTimeToResolution: %v", err)
	}

	if stats.SampleSize != 5 {
		t.Errorf("SampleSize = %d, want 5 — the other team's three issues are out of scope",
			stats.SampleSize)
	}
	if _, leaked := stats.ByPriority["7"]; leaked {
		t.Errorf("ByPriority carries priority 7 (%v) — my team has NO issue at that priority, so "+
			"the per-priority query is answering for the whole workspace", stats.ByPriority)
	}
	if len(stats.ByPriority) != 2 {
		t.Errorf("ByPriority has %d keys (%v), want 2", len(stats.ByPriority), stats.ByPriority)
	}
	if got, ok := stats.ByPriority["1"]; !ok || !closeTo(got, 2) {
		t.Errorf("ByPriority[\"1\"] = %v (present %v), want 2 — my team's median over [1, 2, 100]. "+
			"Unscoped it is 50, the median of [1, 2, 50, 50, 100]", got, ok)
	}
	if got, ok := stats.ByPriority["3"]; !ok || !closeTo(got, 6) {
		t.Errorf("ByPriority[\"3\"] = %v (present %v), want 6", got, ok)
	}
}
