// THE BURNDOWN'S "Off track" BADGE IS Go's ZERO VALUE FOR EVERY CYCLE THAT HAS NOT STARTED YET,
// AND NOTHING IN THE REPOSITORY ASSERTED EITHER OF THE TWO FIELDS THAT MAKE THIS REPORT DIFFERENT
// FROM THE ONE IN internal/cycle.
//
// analytics.BurndownReport has six fields. Four of them — cycle id, name, window and the points
// series — are guarded: TestBurndown_SeriesMatchesAnIndependentOracle pins every Remaining and
// Ideal against a hand-written oracle, and TestAnalytics_Burndown_WorkspaceScoped pins the scope.
// The other two, IsOnTrack and ProjectedEnd, are the "analytics-layer enrichments" the engine's
// own docstring gives as the reason this implementation exists ALONGSIDE cycle.Store.GetBurndown,
// which returns points and nothing else. MEASURED over the FULL suite before this file existed:
// replacing `report.IsOnTrack = remaining <= ideal` with `report.IsOnTrack = true`, and disabling
// the ProjectedEnd block outright, each leave EVERY TEST IN THE REPOSITORY GREEN.
//
// ⚠ AND THE FIRST QUESTION ASKED OF IsOnTrack FOUND A CLASS OF CYCLE WHOSE VERDICT IS NOT A
// MEASUREMENT AT ALL. It is assigned only inside `if !day.After(now)`, so when NO day of the
// window has arrived the loop never assigns it and the report ships `false`. Measured against
// real Postgres on clean main, four cycles seeded through the production INSERT:
//
//	FUTURE  (starts in 7 days, 5 issues, none done)  is_on_track=false   ← no day has arrived
//	FUTURE  (starts in 7 days, NO ISSUES AT ALL)     is_on_track=false   ← nothing to be behind on
//	TODAY   (starts now,       4 issues, none done)  is_on_track=true    ← day 0 arrived
//	LIVE    (started 10d ago, 14 issues, none done)  is_on_track=false   ← genuinely behind
//
// The first two rows are the defect and the third is what makes it one: the SAME cycle, with the
// SAME work in it, flips from "Off track" to "On track" when its start date arrives and nothing
// else changes. frontend/src/components/cycle/BurndownChart.tsx renders the field as a two-state
// badge — `report.is_on_track ? "On track" : "Off track"` — in priority-urgent red, with no third
// state, so an empty sprint scheduled for next week is drawn exactly like a sprint that is late.
// cycle.Store.Create DEFAULTS a new cycle's status to "upcoming" and bounds the window only by
// "end after start", so this is the ordinary state of a planned sprint, not an edge case.
//
// ⚠ THE FIX IS NOT A CHOSEN VERDICT — IT IS THE RULE THE LOOP ALREADY APPLIES, EVALUATED AT THE
// ONLY DAY AVAILABLE. At i=0 the ideal is `total - (total*0)/(days-1)` = total, and remaining is
// `total - completed` which can never exceed total, so day 0 is on-track by construction — which
// is why the TODAY row above reads true with zero work done. A cycle that has not started reports
// what that same cycle reports the instant its first day arrives.
package analytics_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// onTrackFixture seeds a cycle through the real `cycles` table and attaches `total` issues to it,
// of which `done` carry a completed_at INSIDE the window (one day after the window opens, so the
// stamp is never filtered out by the report's own `completed_at <= end-of-last-day` bound).
type onTrackFixture struct {
	d          *testutil.DB
	wsID, tmID string
	n          int
}

func (f *onTrackFixture) cycle(t *testing.T, name string, startDays, endDays, total, done int) string {
	t.Helper()
	ctx := context.Background()
	f.n++
	var id string
	if err := f.d.Pool.QueryRow(ctx,
		`INSERT INTO cycles (team_id, workspace_id, name, number, status, start_date, end_date)
         VALUES ($1,$2,$3,$4,'upcoming',
                 NOW() + make_interval(days => $5), NOW() + make_interval(days => $6))
         RETURNING id`,
		f.tmID, f.wsID, name, f.n, startDays, endDays).Scan(&id); err != nil {
		t.Fatalf("seed cycle %s: %v", name, err)
	}
	for i := 0; i < total; i++ {
		iss := f.d.Issue(t, f.wsID, f.tmID)
		if _, err := f.d.Pool.Exec(ctx,
			`UPDATE issues SET cycle_id = $1 WHERE id = $2`, id, iss.ID); err != nil {
			t.Fatalf("attach issue to %s: %v", name, err)
		}
		if i < done {
			if _, err := f.d.Pool.Exec(ctx,
				`UPDATE issues SET completed_at = NOW() + make_interval(days => $1), status = 'done'
                 WHERE id = $2`, startDays+1, iss.ID); err != nil {
				t.Fatalf("stamp completed_at on %s: %v", name, err)
			}
		}
	}
	return id
}

// currentDay re-derives, from the PUBLISHED series and nothing else, the point the report's own
// docstring calls "the current date": the last day of the window that has arrived. It is an
// oracle for the `!day.After(now)` selection, which is the term that decides WHICH point the
// verdict is read from — a term no other test in the repository touches.
func currentDay(rep *analytics.BurndownReport, now time.Time) (analytics.BurndownPoint, bool) {
	var got analytics.BurndownPoint
	found := false
	for _, p := range rep.Points {
		if !p.Date.After(now) {
			got, found = p, true
		}
	}
	return got, found
}

// TestBurndown_OnTrackAndProjection_AreComputedNotDefaulted guards the two fields that separate
// analytics.GetBurndown from cycle.Store.GetBurndown. Every claim below is a claim about a field
// no test named before this one.
func TestBurndown_OnTrackAndProjection_AreComputedNotDefaulted(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)
	f := &onTrackFixture{d: d, wsID: ws.ID, tmID: tm.ID}
	e := analytics.New(d.Pool)

	get := func(label, id string) *analytics.BurndownReport {
		t.Helper()
		rep, err := e.GetBurndown(ctx, id, ws.ID)
		if err != nil {
			t.Fatalf("GetBurndown(%s): %v", label, err)
		}
		if len(rep.Points) == 0 {
			t.Fatalf("GetBurndown(%s): no points — the fixture, not the assertion, is broken", label)
		}
		return rep
	}

	// ── [B-FUTURE] A CYCLE WHOSE WINDOW HAS NOT STARTED IS NOT BEHIND ANYTHING.
	// 5 issues, none done, starting in 7 days. The badge this drives is rendered in
	// priority-urgent red and reads "Off track".
	future := get("future", f.cycle(t, "Future-Sprint", 7, 21, 5, 0))
	if _, arrived := currentDay(future, time.Now().UTC()); arrived {
		t.Fatalf("[B-FIXTURE] a day of the 'starts in 7 days' window has already arrived — the "+
			"fixture no longer exercises the not-yet-started case: start=%s points[0]=%s",
			future.StartDate, future.Points[0].Date)
	}
	if !future.IsOnTrack {
		t.Errorf("[B-FUTURE] a cycle that starts in 7 days reports is_on_track=false — "+
			"BurndownChart.tsx draws that as an urgent-red \"Off track\" badge. Nothing is late "+
			"before the first day: the report's OWN day 0 says remaining=%d <= ideal=%d.",
			future.Points[0].Remaining, future.Points[0].Ideal)
	}
	// The report's own first point is what makes the line above a contradiction rather than a
	// preference. If this ever fails, the argument changes and so must the assertion.
	if future.Points[0].Remaining > future.Points[0].Ideal {
		t.Errorf("[B-FUTURE-PREMISE] day 0 of the window is itself behind (remaining=%d ideal=%d) — "+
			"day 0's ideal is the full total by construction, so this cannot happen",
			future.Points[0].Remaining, future.Points[0].Ideal)
	}

	// ── [B-EMPTY] The same, with NO WORK IN THE CYCLE AT ALL. There is no reading of "off track"
	// that applies to a sprint with zero issues that has not begun.
	empty := get("empty-future", f.cycle(t, "Empty-Future-Sprint", 7, 21, 0, 0))
	if !empty.IsOnTrack {
		t.Errorf("[B-EMPTY] a future cycle holding NO issues reports is_on_track=false")
	}

	// ── [B-CONTINUITY] THE DISCONTINUITY IS THE PROOF. The same shape, same work, starting today
	// rather than in 7 days, must not disagree.
	today := get("today", f.cycle(t, "Today-Sprint", 0, 14, 5, 0))
	if _, arrived := currentDay(today, time.Now().UTC()); !arrived {
		t.Fatalf("[B-FIXTURE] the 'starts today' cycle has no arrived day — fixture broken")
	}
	if today.IsOnTrack != future.IsOnTrack {
		t.Errorf("[B-CONTINUITY] two cycles with identical work (5 issues, none done) disagree on "+
			"is_on_track purely because one has not started: today=%v future=%v",
			today.IsOnTrack, future.IsOnTrack)
	}

	// ── [B-BEHIND] The negative direction. Without it, `IsOnTrack = true` passes everything above.
	// Started 10 days ago in a 15-day window with 14 issues and none done: at the current day the
	// ideal has burnt most of the way down and remaining has not moved.
	behind := get("behind", f.cycle(t, "Behind-Sprint", -10, 4, 14, 0))
	bp, ok := currentDay(behind, time.Now().UTC())
	if !ok {
		t.Fatalf("[B-FIXTURE] the live 'behind' cycle has no arrived day — fixture broken")
	}
	if bp.Remaining <= bp.Ideal {
		t.Fatalf("[B-FIXTURE] the 'behind' cycle is not behind at its current day "+
			"(remaining=%d ideal=%d) — the assertion below would be vacuous", bp.Remaining, bp.Ideal)
	}
	if behind.IsOnTrack {
		t.Errorf("[B-BEHIND] a live cycle whose current day is behind the ideal line "+
			"(remaining=%d > ideal=%d) reports is_on_track=true", bp.Remaining, bp.Ideal)
	}

	// ── [B-AHEAD] And the other direction on a live cycle, so the field is not simply
	// "false whenever the cycle is running".
	ahead := get("ahead", f.cycle(t, "Ahead-Sprint", -10, 4, 14, 12))
	ap, ok := currentDay(ahead, time.Now().UTC())
	if !ok {
		t.Fatalf("[B-FIXTURE] the live 'ahead' cycle has no arrived day — fixture broken")
	}
	if ap.Remaining > ap.Ideal {
		t.Fatalf("[B-FIXTURE] the 'ahead' cycle is not ahead at its current day "+
			"(remaining=%d ideal=%d)", ap.Remaining, ap.Ideal)
	}
	if !ahead.IsOnTrack {
		t.Errorf("[B-AHEAD] a live cycle at or below the ideal line (remaining=%d <= ideal=%d) "+
			"reports is_on_track=false", ap.Remaining, ap.Ideal)
	}

	// ── [B-ORACLE] The verdict must be read from the CURRENT day, not the first or last one.
	// This re-derives the selection from the published series, so a change to `!day.After(now)`
	// that still produces a plausible boolean is caught rather than absorbed.
	for _, tc := range []struct {
		name string
		rep  *analytics.BurndownReport
	}{{"behind", behind}, {"ahead", ahead}} {
		p, ok := currentDay(tc.rep, time.Now().UTC())
		if !ok {
			t.Fatalf("[B-ORACLE] %s: no arrived day", tc.name)
		}
		if want := p.Remaining <= p.Ideal; tc.rep.IsOnTrack != want {
			t.Errorf("[B-ORACLE] %s: is_on_track=%v but the current day (%s) has remaining=%d "+
				"ideal=%d, i.e. %v", tc.name, tc.rep.IsOnTrack,
				p.Date.Format("2006-01-02"), p.Remaining, p.Ideal, want)
		}
	}

	// ── [B-PROJ-SET] ProjectedEnd is a POINTER and `omitempty`, so a block that never runs is
	// indistinguishable on the wire from a cycle that legitimately has no projection. Partial
	// progress on a live cycle is the case where a projection exists.
	partial := get("partial", f.cycle(t, "Partial-Sprint", -10, 4, 14, 5))
	if partial.ProjectedEnd == nil {
		t.Errorf("[B-PROJ-SET] a live cycle with 5 of 14 done and work remaining has no " +
			"projected_end — the field is omitempty, so an unset pointer disappears from the JSON")
	} else if !partial.ProjectedEnd.After(time.Now().UTC()) {
		t.Errorf("[B-PROJ-SET] projected_end %s is not in the future", partial.ProjectedEnd)
	}

	// ── [B-PROJ-NIL] And it must not be invented where the rate is unmeasurable: a cycle that has
	// not started has no elapsed time to divide by.
	if future.ProjectedEnd != nil {
		t.Errorf("[B-PROJ-NIL] a cycle that has not started reports projected_end=%s — "+
			"there is no completion rate to extrapolate from", future.ProjectedEnd)
	}

	// ── [B-PROJ-DONE] Nor where there is nothing left to finish.
	finished := get("finished", f.cycle(t, "Finished-Sprint", -10, 4, 6, 6))
	fp, ok := currentDay(finished, time.Now().UTC())
	if !ok || fp.Remaining != 0 {
		t.Fatalf("[B-FIXTURE] the 'finished' cycle has remaining=%d at its current day, want 0 "+
			"(arrived=%v) — the assertion below would not be about a completed cycle",
			fp.Remaining, ok)
	}
	if finished.ProjectedEnd != nil {
		t.Errorf("[B-PROJ-DONE] a cycle with nothing remaining reports projected_end=%s",
			finished.ProjectedEnd)
	}

	// Guards the fixture builder itself: if `done` stopped stamping completed_at the cases above
	// would all read as "no work done" and several assertions would go quiet rather than red.
	if got := fmt.Sprintf("%d/%d", ap.Remaining, len(ahead.Points)); ap.Remaining >= 14 {
		t.Errorf("[B-FIXTURE] the 'ahead' cycle burnt nothing (%s) — completed_at is not landing "+
			"inside the window", got)
	}
}
