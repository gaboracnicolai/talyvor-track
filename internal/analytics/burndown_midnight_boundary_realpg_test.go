// THE BURNDOWN'S DAY BOUNDARY IS EXCLUSIVE AND NOTHING IN THIS REPOSITORY REQUIRED THE
// COMPARISONS AGAINST IT TO BE STRICT — THE ONE ROW THAT CAN TELL `<` FROM `<=` HAS NEVER
// EXISTED IN A FIXTURE.
//
// engine.go's dayEndExclusive header states the requirement in capitals — "IT IS EXCLUSIVE, AND
// EVERY COMPARISON AGAINST IT MUST BE STRICT" — and names
// TestBurndown_TheFinalSecondOfADayIsInThatDay_RealPG as the guard. MEASURED at 2957231, that
// claim was false, and the reason is worth more than the fix:
//
//	MUTATION                                             CAUGHT BY
//	the walk's `.Before(eod)` -> `!.After(eod)`           TestBurndown_SeriesMatchesAnIndependentOracle
//	                                                      ONLY — NOT by the test named above
//	completionsThrough's SQL `< $2` -> `<= $2`            NOTHING, whole repository
//
// WHY THE NAMED TEST CANNOT SEE IT. That file seeds completions in the final second of a day
// (23:59:59.5) — the instant that discriminated against the OLD boundary, when endOfDay returned
// an INCLUSIVE 23:59:59. The boundary has since become dayEndExclusive, i.e. MIDNIGHT, and
// 23:59:59.5 is strictly inside the day under `<` and under `<=` alike. The fixture pins the old
// boundary's discriminating instant; when the boundary moved, the instant that separates the two
// predicates moved with it and no fixture followed. The row that discriminates against an
// EXCLUSIVE midnight is a completion at EXACTLY midnight, and the repository has never stored one.
//
// That is the same shape the named file was itself written to close, one boundary later: not an
// unasserted term, but a term made unfalsifiable by the fixtures available to assert it. A guard
// that survives the thing it guards being redefined is not still a guard.
//
// ⚠ THE VALUE IS NOT CONTRIVED. completed_at is TIMESTAMPTZ and issue.Store.Update stamps it from
// time.Now().UTC(), so exact midnight is 1 chance in 86.4 billion per closure — rarer than the
// final second the named file argues for. IT IS NOT SEEDED BECAUSE IT IS LIKELY. It is seeded
// because it is the ONLY instant at which the two predicates disagree, which makes it the only
// fixture that can hold the requirement engine.go writes in capitals. A boundary test's job is to
// stand on the boundary.
//
// ⚠ WHAT IS DELIBERATELY *NOT* ASSERTED HERE: the SQL bound. `completed_at < $2` made fully inert
// is NOT CAUGHT by the whole repository, and that verdict is CORRECT — engine.go:78 says the bound
// is an early-out rather than a correctness term, and the walk below re-excludes anything the read
// lets through. MEASURED rather than reasoned: with the read bound deleted the report is byte-for-
// byte identical. Asserting it here would pin an implementation detail the engine is free to drop.
package analytics_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// TestBurndown_AMidnightCompletionBelongsToTheDayItOPENS_RealPG stands the one row that can tell
// `<` from `<=` on the boundary between day 0 and day 1.
//
// Under the correct STRICT walk (`ct.Before(eod)`), a completion at exactly midnight is NOT before
// day 0's exclusive end, so day 0 does not count it; day 1 does. Under `<=` (`!ct.After(eod)`) day
// 0 swallows it and every subsequent day is unchanged — the series is one closure early, in the
// mirror image of the lag the named file catches.
func TestBurndown_AMidnightCompletionBelongsToTheDayItOPENS_RealPG(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)

	const span = 3 // end_date = start + 3 days => days = 4, points 0..3
	const total = 2
	c := seedSpanCycle(t, db, ws.ID, tm.ID, time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC), span)

	// The frame is the STORED start_date in the CONNECTION's location, and the arithmetic is
	// dayEndExclusive's own — truncate to the day, then add one day. Computing the boundary any
	// other way would be a second implementation of the thing under test.
	start := c.StartDate
	midnightEndingDay0 := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0,
		start.Location()).AddDate(0, 0, 1)
	// An unambiguous row, hours away from any boundary, so the companion assertion below is not
	// itself standing on the instant under test.
	noonOfDay2 := midnightEndingDay0.AddDate(0, 0, 1).Add(12 * time.Hour)

	for _, at := range []time.Time{midnightEndingDay0, noonOfDay2} {
		iss := db.Issue(t, ws.ID, tm.ID)
		if _, err := db.Pool.Exec(ctx,
			`UPDATE issues SET cycle_id = $1, completed_at = $2, status = 'done' WHERE id = $3`,
			c.ID, at, iss.ID); err != nil {
			t.Fatalf("stamp completed_at %s: %v", at, err)
		}
	}

	// ── [M-PREMISE] The fixture's whole claim, measured IN-PROCESS against the database rather
	// than assumed from the Go value. If the driver, the column type or a session TimeZone ever
	// moved this row off midnight by any amount, it would sit strictly inside one day, `<` and
	// `<=` would agree on it, and [M-BOUNDARY] below would pass VACUOUSLY. It fails as a broken
	// fixture instead of passing quietly — the failure mode this whole file exists to name.
	// The instant must BE a day boundary, checked independently of how it was built. Without this
	// the premise only says "stored == intended" and a fixture edited to midnight+1µs would satisfy
	// it, satisfy both assertions below, and silently stop discriminating `<` from `<=` — the test
	// would pass forever while guarding nothing. MEASURED: before this check existed, shifting the
	// fixture one microsecond left the whole file GREEN. Control N5.
	if h, m, sec := midnightEndingDay0.Clock(); h != 0 || m != 0 || sec != 0 || midnightEndingDay0.Nanosecond() != 0 {
		t.Fatalf("[M-PREMISE] the fixture instant %s is not a day boundary in %s. This test "+
			"discriminates `<` from `<=` ONLY on an exact boundary; anywhere else both predicates "+
			"agree and every assertion below passes vacuously.",
			midnightEndingDay0.Format(time.RFC3339Nano), midnightEndingDay0.Location())
	}

	var stored time.Time
	if err := db.Pool.QueryRow(ctx,
		`SELECT completed_at FROM issues WHERE cycle_id = $1 AND completed_at IS NOT NULL
         ORDER BY completed_at LIMIT 1`, c.ID).Scan(&stored); err != nil {
		t.Fatalf("[M-PREMISE] read back the boundary row: %v", err)
	}
	if !stored.Equal(midnightEndingDay0) {
		t.Fatalf("[M-PREMISE] BROKEN FIXTURE: the boundary row stored as %s, wanted exactly %s. "+
			"This test can only tell `<` from `<=` if the row sits EXACTLY on the boundary; off it "+
			"by any amount and both predicates agree and every assertion below is vacuous.",
			stored.Format(time.RFC3339Nano), midnightEndingDay0.Format(time.RFC3339Nano))
	}

	rep, err := analytics.New(db.Pool).GetBurndown(ctx, c.ID, ws.ID)
	if err != nil {
		t.Fatalf("GetBurndown: %v", err)
	}
	if len(rep.Points) != span+1 {
		t.Fatalf("report has %d points, want %d — the window moved and the indices below are "+
			"meaningless", len(rep.Points), span+1)
	}

	// ── [M-BOUNDARY] THE ASSERTION THIS FILE EXISTS FOR. dayEndExclusive(day 0) IS this instant,
	// and the bound is exclusive, so the row belongs to the day the instant OPENS, not the one it
	// closes. Day 0 must still report everything remaining.
	if got := rep.Points[0].Remaining; got != total {
		t.Errorf("[M-BOUNDARY] day 0 reports %d remaining, want %d: the issue completed at exactly "+
			"%s was counted on the day that instant CLOSES. dayEndExclusive returns midnight and the "+
			"walk must compare STRICTLY (`ct.Before(eod)`); `!ct.After(eod)` pulls the boundary row "+
			"one day early. engine.go's dayEndExclusive header states this requirement in capitals.",
			got, total, midnightEndingDay0.Format(time.RFC3339Nano))
	}

	// ── [M-COMPANION] MUST STAY GREEN. Without it [M-BOUNDARY] would also pass on an engine that
	// counted NOTHING — remaining=total on every day satisfies it — so the refusal would be
	// justified by no mutation. Day 1 is where the boundary row belongs and it must be there.
	if got := rep.Points[1].Remaining; got != total-1 {
		t.Errorf("[M-COMPANION] day 1 reports %d remaining, want %d: the boundary completion at %s "+
			"was not counted on the day it OPENS either. [M-BOUNDARY] passing while this fails means "+
			"the row was dropped entirely rather than placed correctly.",
			got, total-1, midnightEndingDay0.Format(time.RFC3339Nano))
	}
}
