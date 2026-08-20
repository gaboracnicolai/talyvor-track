// THE BURNDOWN'S DAY BOUNDARY IS 23:59:59 ON A COLUMN THAT STORES MICROSECONDS, SO THE LAST
// SECOND OF EVERY DAY FALLS THROUGH IT — AND ON THE CYCLE'S FINAL DAY THE WORK IS NOT COUNTED
// LATE, IT IS NEVER COUNTED AT ALL.
//
// engine.go's endOfDay calls itself "the burndown day boundary" and returns
// time.Date(y, m, d, 23, 59, 59, 0, loc) — an instant with ZERO nanoseconds. Both places that
// consume it compare with `<=`:
//
//	engine.go:221  completionsThrough(..., endOfDay(start.AddDate(0, 0, days-1)))
//	               -> SQL `completed_at <= $2`      — decides which rows are READ AT ALL
//	engine.go:249  eod := endOfDay(day)
//	               -> `!completions[completed].After(eod)` — decides which day counts a row
//
// So the half-open second [23:59:59.000000001, 23:59:59.999999999] is on the far side of the
// boundary. For an INTERIOR day that is a one-day lag: the row is counted, on the following day.
// For the cycle's LAST day it is terminal — the query bound is that same instant, so the row is
// never fetched, and no later day exists to absorb it.
//
// ⚠ THE VALUES ARE NOT CONTRIVED. issue.Store.Update stamps `updates["completed_at"] =
// time.Now().UTC()` (store.go:1069) and the column is TIMESTAMPTZ, i.e. microseconds. Every issue
// closed through the product carries a sub-second completed_at; landing in the final second of a
// day is 1 chance in 86,400 per closure, not a value a caller has to construct.
//
// ⚠ WHAT IT COSTS. BurndownReport.Points is what frontend/src/components/cycle/BurndownChart.tsx
// draws, and the final point's Remaining is the one a reader takes as "what was left when the
// cycle ended". IsOnTrack is assigned on the last day that has ARRIVED — for a finished cycle,
// the final day — so a sprint that genuinely closed its last issue can ship is_on_track=false and
// draw the priority-urgent red "Off track" badge, which is the same failure
// burndown_ontrack_realpg_test.go was written for, reached by a different route.
//
// ⚠ WHY NO EXISTING FIXTURE COULD SEE IT — THEY ALL STEP BACK FROM THE BOUNDARY BY HOURS.
// burndown_ordering_realpg_test.go and burndown_fanout_realpg_test.go build every completion as
// `eodOf(i).Add(-2 * time.Hour)`; burndown_ontrack_realpg_test.go seeds whole days. Not one puts a
// completion inside the final second, so `<= 23:59:59` and "the end of the day" are the same
// predicate on every row the suite has ever stored. Same class as #158's single-row cohorts and
// #149's uniform tier fixtures: the term is unfalsifiable by construction, not unasserted.
//
// The two assertions below are separated deliberately because they fail for DIFFERENT reasons:
// [F-FINAL] is a row that is never READ, [F-INTERIOR] is a row read and counted on the wrong day.
// A fix to only one of the two leaves the other red.
package analytics_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// seedFinalSecondCycle builds the one fixture shape no existing burndown test has: a cycle whose
// every issue is closed, one of them inside the FINAL SECOND of the cycle's last day and one
// inside the final second of day 0. Returns the cycle and the two instants.
func seedFinalSecondCycle(t *testing.T, db *testutil.DB, wsID, teamID string, span int) (
	c *model.Cycle, lateOnDay0, lateOnFinalDay time.Time,
) {
	t.Helper()
	ctx := context.Background()
	c = seedSpanCycle(t, db, wsID, teamID, time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC), span)

	// The frame is the STORED start_date in the CONNECTION's location — the trap
	// TestBurndown_SeriesMatchesAnIndependentOracle records, reproduced here for the same reason.
	start := c.StartDate
	lastSecondOf := func(i int) time.Time {
		d := start.AddDate(0, 0, i)
		return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, d.Location())
	}
	lateOnDay0 = lastSecondOf(0).Add(500 * time.Millisecond)
	lateOnFinalDay = lastSecondOf(span).Add(500 * time.Millisecond)

	for _, at := range []time.Time{lateOnDay0, lateOnFinalDay} {
		iss := db.Issue(t, wsID, teamID)
		if _, err := db.Pool.Exec(ctx,
			`UPDATE issues SET cycle_id = $1, completed_at = $2, status = 'done' WHERE id = $3`,
			c.ID, at, iss.ID); err != nil {
			t.Fatalf("stamp completed_at %s: %v", at, err)
		}
	}

	// ── [F-PREMISE] The fixture's whole claim, MEASURED IN-PROCESS against the database rather
	// than assumed from the Go values. If Postgres (or the driver, or a column type change) ever
	// truncated these to whole seconds, both rows would sit exactly ON the old 23:59:59 boundary,
	// `<= 23:59:59` and `< next-midnight` would be the same predicate, and EVERY assertion in this
	// file would pass vacuously. It fails as a BROKEN FIXTURE rather than passing quietly.
	//
	// ⚠ IT LIVES IN THE SEEDER, NOT IN ONE TEST, AND THE CONTROL HARNESS IS WHY. Control B6 moves
	// both completions onto the whole second; with this check inline in a single test it scored
	// NOT CAUGHT, because the OTHER test shared the fixture and had no premise of its own — the
	// exact "guard that cannot fail" shape this file was written to close, reproduced in the file
	// closing it. Every consumer of this seeder now inherits the premise by construction.
	var stored []time.Time
	rows, err := db.Pool.Query(ctx,
		`SELECT completed_at FROM issues WHERE cycle_id = $1 AND completed_at IS NOT NULL
         ORDER BY completed_at`, c.ID)
	if err != nil {
		t.Fatalf("[F-PREMISE] read back: %v", err)
	}
	for rows.Next() {
		var ct time.Time
		if err := rows.Scan(&ct); err != nil {
			rows.Close()
			t.Fatalf("[F-PREMISE] scan: %v", err)
		}
		stored = append(stored, ct)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("[F-PREMISE] rows: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("[F-PREMISE] stored %d completions, want 2 — the fixture is broken", len(stored))
	}
	for i, ct := range stored {
		if ct.Nanosecond() == 0 {
			t.Fatalf("[F-PREMISE] completion %d came back with ZERO sub-second part (%s), so it "+
				"sits ON the 23:59:59 boundary rather than inside the final second — this fixture "+
				"cannot distinguish the two predicates it exists to distinguish", i, ct)
		}
	}
	return c, lateOnDay0, lateOnFinalDay
}

// TestBurndown_BothPortsAgreeOnTheFinalSecond_RealPG — THE RULE IS WRITTEN TWICE AND IS PINNED ONCE.
//
// cycle.Store.GetBurndown carried a byte-identical copy of the same helper and the same two `<=`
// comparisons; analytics.Engine.GetBurndown's own docstring says it is the "same approach ... but
// with the analytics-layer enrichments". The frontend draws the ANALYTICS port
// (frontend/src/api/analytics.ts:15 -> /analytics/burndown), so a fix to that one alone would leave
// GET /v1/workspaces/{wsID}/cycles/{id}/burndown — a mounted, shipping route — answering the old
// numbers, and nothing in the repository compared the two.
//
// ⚠ IT PINS THE VALUES, NOT MERELY THE AGREEMENT. #170 measured why: a mutation that breaks BOTH
// ports identically leaves them agreeing, so an equality-only cross-check reports health for a
// product that is uniformly wrong. Each series is asserted against the arithmetic first and the
// two are compared afterwards.
func TestBurndown_BothPortsAgreeOnTheFinalSecond_RealPG(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	const span = 4
	c, _, lateOnFinalDay := seedFinalSecondCycle(t, db, ws.ID, tm.ID, span)

	fromAnalytics, err := analytics.New(db.Pool).GetBurndown(ctx, c.ID, ws.ID)
	if err != nil {
		t.Fatalf("analytics port: %v", err)
	}
	fromCycle, err := cycle.NewStore(db.Pool).GetBurndown(ctx, c.ID, ws.ID)
	if err != nil {
		t.Fatalf("cycle port: %v", err)
	}
	if len(fromAnalytics.Points) != span+1 || len(fromCycle) != span+1 {
		t.Fatalf("point counts differ from the window: analytics %d, cycle %d, want %d each",
			len(fromAnalytics.Points), len(fromCycle), span+1)
	}

	// ── [P-VALUE] The arithmetic, asserted on EACH port independently. Both issues close inside
	// the window, so the final point of both series is 0 remaining.
	if got := fromAnalytics.Points[span].Remaining; got != 0 {
		t.Errorf("[P-VALUE] analytics port: final remaining %d, want 0 (the closure at %s is in "+
			"the last day's final second)", got, lateOnFinalDay)
	}
	if got := fromCycle[span].Remaining; got != 0 {
		t.Errorf("[P-VALUE] cycle port: final remaining %d, want 0 (the closure at %s is in the "+
			"last day's final second)", got, lateOnFinalDay)
	}

	// ── [P-PARITY] And the two ports answer the SAME series. This is the half that catches a fix
	// applied to one copy of the rule and not the other.
	for i := range fromAnalytics.Points {
		a, cy := fromAnalytics.Points[i], fromCycle[i]
		if a.Remaining != cy.Remaining || a.Ideal != cy.Ideal {
			t.Errorf("[P-PARITY] day %d: analytics {remaining %d, ideal %d} vs cycle "+
				"{remaining %d, ideal %d} — one rule, two ports, and they have drifted",
				i, a.Remaining, a.Ideal, cy.Remaining, cy.Ideal)
		}
	}
}

func TestBurndown_TheFinalSecondOfADayIsInThatDay_RealPG(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	const span = 4 // EndDate = start+4d, so the report has 5 points, indices 0..4

	// ⚠ THE CYCLE FINISHES ALL OF ITS WORK, AND THAT IS WHAT MAKES [F-ONTRACK] FALSIFIABLE.
	// The first cut of this file left two issues open. At the final day the ideal line is 0, so
	// "remaining <= ideal" is false for ANY positive remaining and the badge reads "Off track"
	// whether or not the late row is counted — the assertion could not have failed, which is the
	// one property that disqualifies it. With every issue closed, correct behaviour is
	// remaining=0 <= ideal=0 (on track) and the defect is remaining=1 (off track).
	const total = 2
	c, lateOnDay0, lateOnFinalDay := seedFinalSecondCycle(t, db, ws.ID, tm.ID, span)

	rep, err := analytics.New(db.Pool).GetBurndown(ctx, c.ID, ws.ID)
	if err != nil {
		t.Fatalf("GetBurndown: %v", err)
	}
	if len(rep.Points) != span+1 {
		t.Fatalf("report has %d points, want %d — the window moved and the indices below are "+
			"meaningless", len(rep.Points), span+1)
	}

	// ── [F-FINAL] The row that is never READ. The query's `through` bound is endOfDay of the last
	// day, so a completion inside that day's final second is outside the result set entirely and
	// no later day can absorb it. Every issue in this cycle is closed inside the window, so the
	// last point must report nothing remaining.
	if got := rep.Points[span].Remaining; got != 0 {
		t.Errorf("[F-FINAL] the final point reports %d remaining, want 0: an issue completed at "+
			"%s — inside the cycle's LAST day — was not counted. endOfDay returns 23:59:59.000000000 "+
			"and completionsThrough bounds the read with `completed_at <= $2`, so the final second "+
			"of the final day is unreadable and the work never appears in the report at all.",
			got, lateOnFinalDay)
	}

	// ── [F-INTERIOR] The row that IS read and counted on the WRONG day. Day 0's completion must
	// be visible at index 0, not at index 1. This is the weaker half — the number self-corrects the
	// next day — and it is asserted separately because a fix that only widened the query bound
	// would leave the walk's `!ct.After(eod)` still one second short and only this half red.
	if got := rep.Points[0].Remaining; got != total-1 {
		t.Errorf("[F-INTERIOR] day 0 reports %d remaining, want %d: the issue completed at %s is "+
			"inside day 0 and the walk's boundary `!completions[i].After(endOfDay(day))` puts it in "+
			"day 1. The series is a day late for every closure in a day's final second.",
			got, total-1, lateOnDay0)
	}

	// ── [F-ONTRACK] The consequence the product actually draws, and the reason this is a defect
	// rather than a rounding curiosity. Every day of this cycle is in 2021, so the loop assigns
	// IsOnTrack on the FINAL day, where the ideal line has reached 0. A cycle that closed ALL of
	// its work is on track by the loop's own rule (remaining 0 <= ideal 0); dropping the last
	// closure makes it remaining 1 <= ideal 0 — false — and
	// frontend/src/components/cycle/BurndownChart.tsx draws that as the priority-urgent red
	// "Off track" badge, with no third state. A finished sprint reported as late.
	//
	// The literal `true` is deliberate: computing the expectation from rep.Points[span] would
	// derive it from the same number the defect corrupts, which is how the first cut of this
	// assertion became unfalsifiable.
	if !rep.IsOnTrack {
		t.Errorf("[F-ONTRACK] is_on_track=false for a cycle that completed every one of its %d "+
			"issues inside the window. The final point reports Remaining=%d against Ideal=%d; the "+
			"missing closure is the one at %s, in the final second of the last day.",
			total, rep.Points[span].Remaining, rep.Points[span].Ideal, lateOnFinalDay)
	}
}
