package analytics_test

// ONE BURNDOWN REQUEST ISSUED ONE DATABASE QUERY PER CALENDAR DAY OF A CYCLE WHOSE SPAN NOTHING
// BOUNDS, AND TWO INDEPENDENT IMPLEMENTATIONS DID IT.
//
// analytics.Engine.GetBurndown and cycle.Store.GetBurndown both walk `days` — derived from the
// cycle's own start_date/end_date — and run `SELECT COUNT(*) … completed_at <= $eod` inside the
// loop. cycle.Store.Create and Update validate ONLY `EndDate.After(StartDate)`: there is no span
// bound anywhere, so `days` is caller-set.
//
// MEASURED at 653081eb against real Postgres, statements counted from pg_stat_database.xact_commit
// (flushed — see xactCommit), issues table EMPTY, loopback socket:
//
//	span        points    statements (analytics / cycle)   wall
//	30 d        31        36 / 38                          ~5 ms
//	365 d       366       368 / 370                        ~40 ms
//	3,650 d     3,651     3,653 / 3,655                    ~0.38 s
//	36,500 d    36,501    36,503 / 36,522                  ~3.8 s
//	365,000 d   106,752   106,754 / 106,756                ~11.2 s
//
// ⚠ THE LAST ROW IS THE CEILING AND IT IS AN ACCIDENT OF int64, NOT A BOUND IN THE CODE. `days` is
// `int(end.Sub(start).Hours()/24)+1`, and time.Duration saturates at ~292 years, so a cycle ending
// in 3019 and one ending in 294276 both render 106,752 points. Nothing in cycle.Store, either
// handler, or the analytics engine caps the span.
//
// ⚠ AND NOTHING CUTS THE REQUEST OFF: cmd/track/main.go sets ReadHeaderTimeout and NO WriteTimeout,
// so the handler runs to completion. Both routes are mounted and reachable by any workspace member
// (`GET /analytics/burndown?cycle_id=…`, which frontend/src/api/analytics.ts#burndown calls, and
// `GET /cycles/{id}/burndown`).
//
// THE FIX IS ONE QUERY, NOT A CAP. The per-day predicate is `completed_at <= eod_i` with eod_i
// increasing, i.e. a cumulative count — so the completed timestamps are read ONCE, ordered, and
// merge-walked in Go against the SAME Go-computed day boundaries. Nothing is reinterpreted in a
// timezone the old code did not use, and no number is invented. What the report RENDERS is
// unchanged, which is what TestBurndown_SeriesMatchesAnIndependentOracle exists to hold.
//
// ⚠ WHAT THIS DOES NOT FIX, SO IT IS NOT MISTAKEN FOR FIXED: a 365,000-day cycle still renders
// 106,752 points and still serialises them. Whether a cycle's span should be bounded (and at what)
// is a threshold on a product surface — written up in the queue, not chosen here.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// countingPool is the instrument: a pool of this test database with a pgx QueryTracer on it, so
// every statement the code under test issues is counted EXACTLY, at the client, as it is issued.
//
// ⚠ THE FIRST INSTRUMENT WAS pg_stat_database.xact_commit AND IT WAS NOT SOUND — recorded because
// the number it produced looked completely respectable. pg_stat is backend-local until flushed;
// pg_stat_force_next_flush() flushes at the END of the transaction it runs in, so each touched
// backend lags by one, and WHICH of a ten-connection pool's backends were touched varies per run.
// Measured: seven statements read as six, then as seven, then as six. It over-reports nothing and
// under-reports sometimes, which is the direction that makes a fan-out look smaller than it is.
// The positive control below is what caught it — it was written before the instrument was trusted,
// and the instrument was replaced rather than the control loosened.
type countingPool struct {
	*pgxpool.Pool
	n atomic.Int64
}

func (c *countingPool) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}
func (c *countingPool) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// newCountingPool opens a SECOND pool onto the same database with the tracer attached. The config is
// copied from the harness's own pool, so it points at the same isolated database with the same
// credentials and nothing about the fixture is re-derived.
func newCountingPool(t *testing.T, db *testutil.DB) *countingPool {
	t.Helper()
	cfg := db.Pool.Config().Copy()
	cp := &countingPool{}
	cfg.ConnConfig.Tracer = cp
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("counting pool: %v", err)
	}
	cp.Pool = pool
	t.Cleanup(pool.Close)
	return cp
}

// TestBurndownInstrument_CountsWhatItClaims is the positive control on the counter. A fan-out
// measured as "constant" is unreadable unless the instrument is known to move by exactly K when K
// statements are issued — including K = 0.
func TestBurndownInstrument_CountsWhatItClaims(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	cp := newCountingPool(t, db)

	for _, n := range []int64{0, 1, 7, 50} {
		before := cp.n.Load()
		for i := int64(0); i < n; i++ {
			var one int
			if err := cp.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
				t.Fatal(err)
			}
		}
		if got := cp.n.Load() - before; got != n {
			t.Fatalf("instrument is not linear: %d statements measured as %d "+
				"(a fan-out this counter cannot see reads as no fan-out)", n, got)
		}
	}
	// AND IT MUST NOT COUNT WHAT THE OTHER POOL DOES — the fixture is seeded through db.Pool, and
	// an instrument that counted those too would report a fan-out that is really fixture setup.
	before := cp.n.Load()
	var one int
	if err := db.Pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatal(err)
	}
	if got := cp.n.Load() - before; got != 0 {
		t.Fatalf("instrument counts statements it did not issue: %d", got)
	}
}

// burndownSpans are the two spans every fan-out case is measured at. 3,650 days is 100x 36 and is
// still a cycle a client can create today; if the query count tracks the span, the difference is
// unmissable, and if it does not, the two numbers are equal.
const (
	shortSpanDays = 36
	longSpanDays  = 3650
)

func seedSpanCycle(t *testing.T, db *testutil.DB, wsID, teamID string, start time.Time, spanDays int) *model.Cycle {
	t.Helper()
	c, err := cycle.NewStore(db.Pool).Create(context.Background(), model.Cycle{
		WorkspaceID: wsID, TeamID: teamID,
		Name:      "burndown fan-out fixture",
		StartDate: start,
		EndDate:   start.AddDate(0, 0, spanDays),
	})
	if err != nil {
		t.Fatalf("seed cycle (span %d days): %v", spanDays, err)
	}
	return c
}

// TestAnalyticsBurndown_QueryCountDoesNotTrackTheCycleSpan is the finding, stated as the property
// that was false: two cycles differing ONLY in end_date must cost the same number of statements.
func TestAnalyticsBurndown_QueryCountDoesNotTrackTheCycleSpan(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	cp := newCountingPool(t, db)
	eng := analytics.New(cp.Pool)
	start := time.Date(2021, 3, 1, 0, 0, 0, 0, time.UTC)

	short := seedSpanCycle(t, db, ws.ID, tm.ID, start, shortSpanDays)
	long := seedSpanCycle(t, db, ws.ID, tm.ID, start, longSpanDays)

	measure := func(c *model.Cycle) (int64, int) {
		before := cp.n.Load()
		rep, err := eng.GetBurndown(ctx, c.ID, ws.ID)
		if err != nil {
			t.Fatalf("GetBurndown: %v", err)
		}
		return cp.n.Load() - before, len(rep.Points)
	}

	shortN, shortPts := measure(short)
	longN, longPts := measure(long)

	// ANTI-VACUITY: a burndown that renders nothing would also be constant-cost. The spans must
	// still produce the point counts they always did.
	if shortPts != shortSpanDays+1 || longPts != longSpanDays+1 {
		t.Fatalf("point counts changed: short=%d (want %d), long=%d (want %d)",
			shortPts, shortSpanDays+1, longPts, longSpanDays+1)
	}
	if shortN != longN {
		t.Fatalf("query count tracks the cycle span: %d-day cycle cost %d statements, %d-day cycle cost %d "+
			"(one query per calendar day)", shortSpanDays, shortN, longSpanDays, longN)
	}
}

// TestCycleBurndown_QueryCountDoesNotTrackTheCycleSpan is the same property for the OTHER
// implementation. Named separately so a fix to one is not read as a fix to both.
func TestCycleBurndown_QueryCountDoesNotTrackTheCycleSpan(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	cp := newCountingPool(t, db)
	cs := cycle.NewStore(cp.Pool)
	start := time.Date(2021, 3, 1, 0, 0, 0, 0, time.UTC)

	short := seedSpanCycle(t, db, ws.ID, tm.ID, start, shortSpanDays)
	long := seedSpanCycle(t, db, ws.ID, tm.ID, start, longSpanDays)

	measure := func(c *model.Cycle) (int64, int) {
		before := cp.n.Load()
		pts, err := cs.GetBurndown(ctx, c.ID, ws.ID)
		if err != nil {
			t.Fatalf("GetBurndown: %v", err)
		}
		return cp.n.Load() - before, len(pts)
	}

	shortN, shortPts := measure(short)
	longN, longPts := measure(long)

	if shortPts != shortSpanDays+1 || longPts != longSpanDays+1 {
		t.Fatalf("point counts changed: short=%d (want %d), long=%d (want %d)",
			shortPts, shortSpanDays+1, longPts, longSpanDays+1)
	}
	if shortN != longN {
		t.Fatalf("query count tracks the cycle span: %d-day cycle cost %d statements, %d-day cycle cost %d "+
			"(one query per calendar day)", shortSpanDays, shortN, longSpanDays, longN)
	}
}

// TestBurndown_SeriesMatchesAnIndependentOracle is the half that stops the two tests above being
// satisfiable by a burndown that stopped reporting. The oracle is written from the SPECIFICATION
// ("Remaining is total minus the issues completed at or before 23:59:59 on that date"), in Go,
// against the fixture's own known completion instants — it shares no code with either
// implementation, so a mutation of the merge-walk cannot move both.
//
// The completion instants are chosen to sit ON and AROUND the day boundary the old per-day query
// compared against (23:59:59), because that boundary is exactly what a Go-side walk could get
// wrong: 23:59:58 (inside), 23:59:59 (the boundary itself, inclusive), 23:59:59.5 and 00:00:00
// (outside, next day).
func TestBurndown_SeriesMatchesAnIndependentOracle(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	const span = 5
	c := seedSpanCycle(t, db, ws.ID, tm.ID, time.Date(2021, 3, 1, 0, 0, 0, 0, time.UTC), span)

	// ⚠ THE FRAME IS THE STORED start_date, NOT THE ONE HANDED TO Create, AND THE DIFFERENCE IS
	// REAL. start_date is timestamptz; pgx returns it in the CONNECTION's location, so the
	// end-of-day boundary both implementations build with time.Date(..., day.Location()) is
	// 23:59:59 in that location and not in UTC. An oracle written against the UTC literal disagreed
	// with production by one day on exactly the boundary cases below — the oracle was wrong, not the
	// code, and it is recorded because it is the trap in writing this test at all.
	start := c.StartDate

	// ⚠ THE BOUNDARY CASES ARE BUILT FROM THE DAY'S OWN 23:59:59, NOT FROM start+23h59m59s, AND THE
	// FIRST DRAFT GOT THAT WRONG. start_date comes back with a clock component in the connection's
	// location, so start.Add(23h59m59s) is not end-of-day at all — the "on the boundary" case sat
	// two hours into the next day and controls C4/C5 (drop the equality from the walk) came back
	// GREEN against a guard that was supposed to catch exactly that. The mutation caught the test.
	eodOf := func(i int) time.Time {
		d := start.AddDate(0, 0, i)
		return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, d.Location())
	}
	completions := []time.Time{
		eodOf(0).Add(-time.Second),      // day 0, one second inside
		eodOf(1),                        // day 1, EXACTLY the boundary — inclusive
		eodOf(1).Add(time.Second),       // day 1 + 1s, i.e. the next day
		eodOf(2).Add(-12 * time.Hour),   // day 2, midday
		eodOf(4).Add(-time.Millisecond), // day 4, a millisecond inside
		eodOf(span).Add(-6 * time.Hour), // last day
	}
	const uncompleted = 3
	total := len(completions) + uncompleted

	for i := 0; i < total; i++ {
		iss := db.Issue(t, ws.ID, tm.ID)
		if _, err := db.Pool.Exec(ctx,
			`UPDATE issues SET cycle_id = $1 WHERE id = $2`, c.ID, iss.ID); err != nil {
			t.Fatalf("attach issue to cycle: %v", err)
		}
		if i < len(completions) {
			if _, err := db.Pool.Exec(ctx,
				`UPDATE issues SET completed_at = $1, status = 'done' WHERE id = $2`,
				completions[i], iss.ID); err != nil {
				t.Fatalf("stamp completed_at: %v", err)
			}
		}
	}

	// The oracle: for each day of the window, count the completions at or before end-of-day.
	days := span + 1
	wantRemaining := make([]int, days)
	wantIdeal := make([]int, days)
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		eod := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, day.Location())
		done := 0
		for _, ct := range completions {
			if !ct.After(eod) {
				done++
			}
		}
		wantRemaining[i] = total - done
		wantIdeal[i] = total - (total*i)/(days-1)
	}

	rep, err := analytics.New(db.Pool).GetBurndown(ctx, c.ID, ws.ID)
	if err != nil {
		t.Fatalf("analytics GetBurndown: %v", err)
	}
	if len(rep.Points) != days {
		t.Fatalf("analytics: %d points, want %d", len(rep.Points), days)
	}
	for i, p := range rep.Points {
		if p.Remaining != wantRemaining[i] || p.Ideal != wantIdeal[i] {
			t.Errorf("analytics day %d (%s): remaining=%d ideal=%d, want remaining=%d ideal=%d",
				i, p.Date.Format("2006-01-02"), p.Remaining, p.Ideal, wantRemaining[i], wantIdeal[i])
		}
	}

	pts, err := cycle.NewStore(db.Pool).GetBurndown(ctx, c.ID, ws.ID)
	if err != nil {
		t.Fatalf("cycle GetBurndown: %v", err)
	}
	if len(pts) != days {
		t.Fatalf("cycle: %d points, want %d", len(pts), days)
	}
	for i, p := range pts {
		if p.Remaining != wantRemaining[i] || p.Ideal != wantIdeal[i] {
			t.Errorf("cycle day %d (%s): remaining=%d ideal=%d, want remaining=%d ideal=%d",
				i, p.Date.Format("2006-01-02"), p.Remaining, p.Ideal, wantRemaining[i], wantIdeal[i])
		}
	}
}
