// BOTH BURNDOWN IMPLEMENTATIONS DEPEND ON THEIR COMPLETION READ ARRIVING SORTED, AND DROPPING THE
// `ORDER BY completed_at` FROM EITHER ONE LEFT EVERY TEST IN THE REPOSITORY GREEN.
//
// analytics.completionsThrough and cycle.Store.GetBurndown each replaced a per-day COUNT(*) with a
// single read plus a MERGE WALK: `for completed < len(completions) && !completions[completed].After(eod)`.
// That walk stops at the FIRST element past the day boundary, so it is only a cumulative count if
// the slice is non-decreasing. The `ORDER BY` is the whole of that guarantee.
//
// ⚠ THE EXISTING ORACLE TEST CANNOT SEE THIS, AND THE REASON IS ITS FIXTURE RATHER THAN ITS
// ASSERTIONS. TestBurndown_SeriesMatchesAnIndependentOracle is a good test — it pins every
// Remaining and Ideal against a hand-written oracle, including the 23:59:59 boundary cases — but it
// builds its six completion instants in ASCENDING order and stamps them in that same order, so the
// rows are already sorted on disk and an unordered read returns them sorted anyway. MEASURED over
// the FULL suite: removing the `ORDER BY` from analytics.completionsThrough, and separately from
// cycle.Store.GetBurndown, is caught by NOTHING. The population that test exercises is ordered by
// accident of how it was written, which is exactly the kind of boundary a census has to state.
//
// ⚠ AND THE UNORDERED READ IS NOT HYPOTHETICAL — IT IS MEASURED HERE RATHER THAN ASSUMED. With the
// completions stamped newest-first, `EXPLAIN` on the read without an ORDER BY reports
// `Index Scan using idx_issues_cycle` (migrations/0002_issues.sql, a partial index on cycle_id
// alone), every row shares one cycle_id, and the instants come back DESCENDING. The premise check
// below re-measures that in-process on every run: if this database ever hands the rows back sorted
// anyway, this test FAILS as a broken fixture instead of passing while asserting nothing.
package analytics_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/testutil"
)

// TestBurndown_TheCompletionReadMustBeOrdered_RealPG is TestBurndown_SeriesMatchesAnIndependentOracle's
// missing half: the same oracle, over a fixture whose rows are stored OUT of completion order.
func TestBurndown_TheCompletionReadMustBeOrdered_RealPG(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	const span = 6
	c := seedSpanCycle(t, db, ws.ID, tm.ID, time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC), span)

	// ⚠ THE FRAME IS THE STORED start_date, NOT THE LITERAL HANDED TO Create. start_date is
	// timestamptz and pgx returns it in the CONNECTION's location, so end-of-day is 23:59:59 in
	// that location — the trap TestBurndown_SeriesMatchesAnIndependentOracle records, reproduced
	// here for the same reason.
	start := c.StartDate
	eodOf := func(i int) time.Time {
		d := start.AddDate(0, 0, i)
		return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, d.Location())
	}

	// Completion instants in ASCENDING day order — this is the truth the oracle is built from.
	// They are STAMPED in the reverse of this order below, which is the only difference from the
	// sibling test and the entire point of this one.
	completions := []time.Time{
		eodOf(0).Add(-2 * time.Hour),
		eodOf(1).Add(-2 * time.Hour),
		eodOf(2).Add(-2 * time.Hour),
		eodOf(4).Add(-2 * time.Hour),
		eodOf(5).Add(-2 * time.Hour),
		eodOf(6).Add(-2 * time.Hour),
	}
	const uncompleted = 3
	total := len(completions) + uncompleted

	// Attach the uncompleted issues FIRST, then the completed ones newest-first, so the heap order
	// of the rows carrying a completed_at is the exact reverse of `completions`.
	for i := 0; i < uncompleted; i++ {
		iss := db.Issue(t, ws.ID, tm.ID)
		if _, err := db.Pool.Exec(ctx, `UPDATE issues SET cycle_id = $1 WHERE id = $2`, c.ID, iss.ID); err != nil {
			t.Fatalf("attach uncompleted issue: %v", err)
		}
	}
	for i := len(completions) - 1; i >= 0; i-- {
		iss := db.Issue(t, ws.ID, tm.ID)
		if _, err := db.Pool.Exec(ctx,
			`UPDATE issues SET cycle_id = $1, completed_at = $2, status = 'done' WHERE id = $3`,
			c.ID, completions[i], iss.ID); err != nil {
			t.Fatalf("stamp completed_at (descending pass, i=%d): %v", i, err)
		}
	}

	// ── [O-PREMISE] The fixture's whole claim, MEASURED IN-PROCESS. Read the completions the way
	// an unordered implementation would and require that they come back out of order. Without this
	// the test could pass on a database that sorted them anyway, and it would look identical to a
	// test that had caught something.
	rows, err := db.Pool.Query(ctx,
		`SELECT completed_at FROM issues WHERE cycle_id = $1 AND completed_at IS NOT NULL`, c.ID)
	if err != nil {
		t.Fatalf("[O-PREMISE] unordered read: %v", err)
	}
	var raw []time.Time
	for rows.Next() {
		var ct time.Time
		if err := rows.Scan(&ct); err != nil {
			rows.Close()
			t.Fatalf("[O-PREMISE] scan: %v", err)
		}
		raw = append(raw, ct)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("[O-PREMISE] rows: %v", err)
	}
	if len(raw) != len(completions) {
		t.Fatalf("[O-PREMISE] unordered read returned %d rows, want %d — the fixture, not the "+
			"assertion, is broken", len(raw), len(completions))
	}
	ascending := true
	for i := 1; i < len(raw); i++ {
		if raw[i].Before(raw[i-1]) {
			ascending = false
		}
	}
	if ascending {
		t.Fatalf("[O-PREMISE] the read WITHOUT an ORDER BY came back ascending, so this fixture "+
			"cannot distinguish an ordered implementation from an unordered one. It is a broken "+
			"fixture, NOT a passing guard — the storage order this test depends on has changed. "+
			"got: %v", raw)
	}

	// ── The oracle: for each day of the window, count the completions at or before end-of-day.
	// Written from `completions`, which is sorted by construction and never read back from the
	// database, so a mutation of the read cannot move both sides.
	days := span + 1
	wantRemaining := make([]int, days)
	wantIdeal := make([]int, days)
	for i := 0; i < days; i++ {
		eod := eodOf(i)
		done := 0
		for _, ct := range completions {
			if !ct.After(eod) {
				done++
			}
		}
		wantRemaining[i] = total - done
		wantIdeal[i] = total - (total*i)/(days-1)
	}

	// ── [O-ANALYTICS] and [O-CYCLE] — BOTH implementations, because both carry their own copy of
	// the read and the walk, and a fix applied to one would leave the other silently wrong.
	rep, err := analytics.New(db.Pool).GetBurndown(ctx, c.ID, ws.ID)
	if err != nil {
		t.Fatalf("analytics GetBurndown: %v", err)
	}
	if len(rep.Points) != days {
		t.Fatalf("[O-ANALYTICS] %d points, want %d", len(rep.Points), days)
	}
	for i, p := range rep.Points {
		if p.Remaining != wantRemaining[i] || p.Ideal != wantIdeal[i] {
			t.Errorf("[O-ANALYTICS] day %d (%s): remaining=%d ideal=%d, want remaining=%d ideal=%d "+
				"— the completion read is being walked in storage order, not completion order",
				i, p.Date.Format("2006-01-02"), p.Remaining, p.Ideal, wantRemaining[i], wantIdeal[i])
		}
	}

	pts, err := cycle.NewStore(db.Pool).GetBurndown(ctx, c.ID, ws.ID)
	if err != nil {
		t.Fatalf("cycle GetBurndown: %v", err)
	}
	if len(pts) != days {
		t.Fatalf("[O-CYCLE] %d points, want %d", len(pts), days)
	}
	for i, p := range pts {
		if p.Remaining != wantRemaining[i] || p.Ideal != wantIdeal[i] {
			t.Errorf("[O-CYCLE] day %d (%s): remaining=%d ideal=%d, want remaining=%d ideal=%d "+
				"— the completion read is being walked in storage order, not completion order",
				i, p.Date.Format("2006-01-02"), p.Remaining, p.Ideal, wantRemaining[i], wantIdeal[i])
		}
	}
}
