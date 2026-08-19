package analytics_test

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// THE CLAMP HAD A UNIT TEST AND FOUR OF ITS FIVE CALL SITES HAD NOTHING.
//
// `clampDays` bounds every `days` window to [1, 365] and `GetVelocity` bounds `cycles` to [1, 50].
// `TestClampDays_BoundsRespected` (engine_test.go) calls the FUNCTION, and #154's own header records
// D7 — "clampDays' ceiling" — as CAUGHT by that test and by nothing else. A unit test of a function
// cannot see whether anything still CALLS it. Measured rather than read
// (scripts/w34-window-clamp-wiring-controls-6c1a.py), one call site at a time, over the whole call
// closure of these reports — ./internal/analytics/ ./internal/importer/ ./internal/mcp/:
//
//	M1  GetDistribution stops clamping its window      NOT CAUGHT
//	M2  GetTimeToResolution stops clamping its window  CAUGHT  (see below — and NOT from this package)
//	M3  GetAICostTrends stops clamping its window      NOT CAUGHT
//	M4  GetVelocity loses its 50-cycle ceiling         NOT CAUGHT
//	M5  GetVelocity loses its non-positive floor       NOT CAUGHT
//
// ⚠ M2 IS THE ONE THAT MAKES THIS FILE'S SHAPE CREDIBLE RATHER THAN INVENTED, AND MY PREDICTION FOR
// IT WAS WRONG. I predicted all five NOT CAUGHT. M2 reds — from
// `TestResolutionReport_AnImportedBacklogOlderThanTheWindowIsNotAMeasuredZero` in **internal/importer**,
// which calls the report at `days=100000` with a 400-day-old row and asserts `median_hours == 0`. So
// exactly one of the three `days` reports already had a wiring guard, written for a different item,
// living in another package. This file is that same assertion applied to the two windows and the two
// cycle bounds it was never applied to. It deliberately does NOT re-guard resolution: that would be a
// duplicate, and the honest record is that resolution's clamp is guarded from internal/importer and
// would be unguarded again if that test were narrowed.
//
// ⚠ WHAT THESE ASSERTIONS ARE KEYED ON. Every one is a VALUE the product serves, not a query text and
// not the clamp's own return: an out-of-range `days` that reaches the SQL changes WHICH ROWS ARE IN
// THE COHORT, and for ai-costs it also changes `projected_monthly_usd`, which divides by the window.
// The VOID control (the distribution clamp applied twice, arithmetically identity) scores NOT CAUGHT
// against this file, which is the evidence these are keyed on answers rather than on the call's shape.
//
// ⚠ THIS FILE PINS CURRENT, DELIBERATE BEHAVIOUR AND ASSERTS NO DEFECT. maxWindowDays, the 50-cycle
// ceiling and the defaults are the product decisions #93 wrote up. The point is that until now they
// could each be deleted from the call site with the entire closure green.

const (
	// Older than maxWindowDays, so it is outside EVERY window a caller may legally request. 400 not
	// 366: a boundary row would make a red ambiguous between "the clamp is gone" and "the boundary
	// moved by a day".
	clampOutsideDays = 400
	// Inside 30 (the default) and therefore inside every clamped window.
	clampInsideDays = 5
	// Outside 30 and inside 365. This is what separates "clamped to 365" from "clamped to the
	// default" — a row here is absent at days=0 and present at days=100000.
	clampMidDays = 200

	clampWideRequest = 100000 // > maxWindowDays; clamps to 365
)

func seedClampIssue(t *testing.T, d *testutil.DB, wsID, teamID string, n int, status string, ageDays int, cost float64) {
	t.Helper()
	// created_at AND updated_at are set to the same age on purpose HERE, unlike the distribution
	// counting fixture: the two reports under test key their windows on DIFFERENT columns
	// (distribution on created_at, ai-costs on updated_at) and this file is about the window's
	// BOUND, not about which column it reads. Ageing both lets one row serve both reports without
	// this file taking a position on a question distribution_counting_realpg_test.go already owns.
	age := fmt.Sprintf("NOW() - INTERVAL '%d days'", ageDays)
	_, err := d.Pool.Exec(context.Background(), `
        INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
                            creator_id, labels, ai_cost_usd, ai_tokens, created_at, updated_at)
        VALUES ($1, $2, $3::int, 'WC-' || $3::int, 'clamp ' || $3::int, $4, 0, 'wcprobe',
                ARRAY[]::text[], $5, 4242, `+age+`, `+age+`)`,
		wsID, teamID, n, status, cost)
	if err != nil {
		t.Fatalf("seed issue %d (%s, %dd old): %v", n, status, ageDays, err)
	}
}

func clampBucket(out []analytics.DistributionBucket, label string) (analytics.DistributionBucket, bool) {
	for _, b := range out {
		if b.Label == label {
			return b, true
		}
	}
	return analytics.DistributionBucket{}, false
}

// TestGetDistribution_TheWindowClampIsWired_RealPG drives GetDistribution at both ends of clampDays
// and asserts the COHORT each end produces.
func TestGetDistribution_TheWindowClampIsWired_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// Three distinct statuses, so a bucket's presence names WHICH row entered and a count can never
	// be satisfied by the wrong cohort.
	seedClampIssue(t, d, ws.ID, team.ID, 1, "wc_recent", clampInsideDays, 1.00)
	seedClampIssue(t, d, ws.ID, team.ID, 2, "wc_mid", clampMidDays, 2.00)
	seedClampIssue(t, d, ws.ID, team.ID, 3, "wc_ancient", clampOutsideDays, 4.00)

	e := analytics.New(d.Pool)

	// ── PREMISE. All three rows exist and the report is alive. Without this, every absence below
	// would also hold for a report that returns nothing at all.
	all, err := e.GetDistribution(ctx, ws.ID, "status", 365)
	if err != nil {
		t.Fatalf("GetDistribution(365): %v", err)
	}
	if _, ok := clampBucket(all, "wc_recent"); !ok {
		t.Fatalf("PREMISE FAILED: the 5-day-old issue is missing from a 365-day report — the fixture "+
			"did not land or the report is dead, so no absence below proves anything (buckets: %v)", all)
	}
	if _, ok := clampBucket(all, "wc_mid"); !ok {
		t.Fatalf("PREMISE FAILED: the %d-day-old issue is missing from a 365-day report, so this file "+
			"cannot tell a 365-day window from a 30-day one", clampMidDays)
	}

	// ── THE CEILING, KEYED ON A ROW. days=100000 must be served as 365, so the 400-day-old issue is
	// outside it. Unclamped, the window is ~274 years and that row enters.
	wide, err := e.GetDistribution(ctx, ws.ID, "status", clampWideRequest)
	if err != nil {
		t.Fatalf("GetDistribution(%d): %v", clampWideRequest, err)
	}
	if b, ok := clampBucket(wide, "wc_ancient"); ok {
		t.Errorf("[W-DIST-CEILING] a %d-day-old issue is in a days=%d distribution report (count %d). "+
			"maxWindowDays is %d, so the widest cohort a caller can ask for ends at 365 days — this row "+
			"is only reachable if GetDistribution stopped calling clampDays and put the caller's own "+
			"number into the SQL", clampOutsideDays, clampWideRequest, b.Count, 365)
	}
	if _, ok := clampBucket(wide, "wc_mid"); !ok {
		t.Errorf("[W-DIST-CEILING] the %d-day-old issue is ABSENT from a days=%d report. The clamp is "+
			"365, not the 30-day default, so this row must be in it — a window narrower than 365 here "+
			"means the ceiling is being applied as a floor", clampMidDays, clampWideRequest)
	}

	// ── THE FLOOR, KEYED ON A ROW. days=0 must be served as the 30-day default. Unclamped, the
	// window is `NOW() - 0 days` and NOTHING is in it, including the 5-day-old row.
	zero, err := e.GetDistribution(ctx, ws.ID, "status", 0)
	if err != nil {
		t.Fatalf("GetDistribution(0): %v", err)
	}
	if _, ok := clampBucket(zero, "wc_recent"); !ok {
		t.Errorf("[W-DIST-FLOOR] a days=0 distribution report does not contain the 5-day-old issue. "+
			"clampDays turns a non-positive window into the %d-day default, so this report must be the "+
			"default one; an empty answer means the caller's 0 reached the SQL and the window collapsed "+
			"to this instant (buckets: %v)", 30, zero)
	}
	if _, ok := clampBucket(zero, "wc_mid"); ok {
		t.Errorf("[W-DIST-FLOOR] the %d-day-old issue is in a days=0 report, so days=0 was not served "+
			"as the %d-day default", clampMidDays, 30)
	}
}

// TestGetAICostTrends_TheWindowClampIsWired_RealPG asserts the ceiling on the ai-cost report twice:
// once on the COHORT and once on `projected_monthly_usd`, which divides by the window itself.
func TestGetAICostTrends_TheWindowClampIsWired_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// Costs chosen so every sum below is distinct and no mutation can land on another's value:
	// in-window 8.00, mid 16.00, ancient 32.00.
	seedClampIssue(t, d, ws.ID, team.ID, 1, "todo", clampInsideDays, 8.00)
	seedClampIssue(t, d, ws.ID, team.ID, 2, "todo", clampMidDays, 16.00)
	seedClampIssue(t, d, ws.ID, team.ID, 3, "todo", clampOutsideDays, 32.00)

	e := analytics.New(d.Pool)

	wide, err := e.GetAICostTrends(ctx, ws.ID, clampWideRequest)
	if err != nil {
		t.Fatalf("GetAICostTrends(%d): %v", clampWideRequest, err)
	}

	// ── PREMISE. The report is alive and the 365-day cohort is the two newer rows.
	if wide.TotalCostUSD == 0 {
		t.Fatalf("PREMISE FAILED: total_cost_usd is 0 at days=%d — the fixture did not land or the "+
			"report is dead, so the assertions below would pass on nothing", clampWideRequest)
	}

	// ── THE CEILING, ON MONEY. 8 + 16 = 24. The 400-day-old row's 32.00 is outside every legal
	// window; unclamped it enters and the customer is shown 56.00.
	if got := wide.TotalCostUSD; math.Abs(got-24.00) > 0.005 {
		t.Errorf("[W-AICOST-CEILING] total_cost_usd = %.2f at days=%d, want 24.00 (the %d-day and "+
			"%d-day rows). 56.00 means the %d-day-old row entered, which is only possible if "+
			"GetAICostTrends stopped calling clampDays; 8.00 means the window was the 30-day default",
			got, clampWideRequest, clampInsideDays, clampMidDays, clampOutsideDays)
	}

	// ── THE CEILING, ON THE ARITHMETIC THAT DIVIDES BY IT. ProjectedMonthly is (total/days)*30 with
	// days AFTER the clamp. This assertion reds on an unwired clamp even if the cohort were
	// unchanged, because 100000 in the denominator is a different number from 365.
	wantProj := (24.00 / 365.0) * 30.0
	if got := wide.ProjectedMonthly; math.Abs(got-wantProj) > 0.005 {
		t.Errorf("[W-AICOST-PROJECTION] projected_monthly_usd = %.4f at days=%d, want %.4f "+
			"(= total/365*30). This figure divides by the window AFTER the clamp, so an unclamped "+
			"days=%d yields %.4f — a monthly spend projection ~274x too small, served as money",
			got, clampWideRequest, wantProj, clampWideRequest, (24.00/float64(clampWideRequest))*30.0)
	}
}

// TestGetVelocity_TheCycleBoundsAreWired_RealPG drives GetVelocity's ceiling and floor. Neither goes
// through clampDays — they are two inline `if`s — and neither had an assertion.
func TestGetVelocity_TheCycleBoundsAreWired_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// 51 cycles: one more than the ceiling, so "50" is a number the fixture can distinguish from
	// "everything". Numbered 1..51 and the report orders by number DESC.
	const seeded = 51
	for n := 1; n <= seeded; n++ {
		seedVelocityCycle(t, d, ws.ID, team.ID, fmt.Sprintf("wc-cycle-%02d", n), n)
	}

	e := analytics.New(d.Pool)

	// ── PREMISE. All 51 landed and the report can see them.
	if got, err := e.GetVelocity(ctx, team.ID, ws.ID, 50); err != nil {
		t.Fatalf("GetVelocity(50): %v", err)
	} else if len(got) != 50 {
		t.Fatalf("PREMISE FAILED: an in-range request for 50 cycles returned %d — the fixture did not "+
			"land (%d cycles expected), so the bound assertions below prove nothing", len(got), seeded)
	}

	// ── THE CEILING. cycles=100 must be served as 50, not as "all 51".
	over, err := e.GetVelocity(ctx, team.ID, ws.ID, 100)
	if err != nil {
		t.Fatalf("GetVelocity(100): %v", err)
	}
	if len(over) != 50 {
		t.Errorf("[W-VEL-CEILING] a request for 100 cycles returned %d rows, want 50. %d rows means "+
			"the caller's number reached `LIMIT $3` and the 50-cycle ceiling is not applied",
			len(over), seeded)
	}

	// ── THE FLOOR AT ZERO. cycles=0 must be served as the 5-cycle default. Without the floor the
	// statement is `LIMIT 0` and the report is empty — an answer indistinguishable from a team with
	// no cycles at all.
	zero, err := e.GetVelocity(ctx, team.ID, ws.ID, 0)
	if err != nil {
		t.Fatalf("GetVelocity(0): %v", err)
	}
	if len(zero) != 5 {
		t.Errorf("[W-VEL-FLOOR] a request for 0 cycles returned %d rows, want the 5-cycle default. "+
			"0 rows is `LIMIT 0` reaching Postgres — a team with 51 cycles reported as a team with none",
			len(zero))
	}

	// ── THE FLOOR BELOW ZERO. `?cycles=-1` survives handler.intParam unchanged (it only falls back
	// on a PARSE error), so a negative reaches this method from the wire. Postgres refuses a negative
	// LIMIT, so without the floor this is a 500 on caller input rather than a report.
	neg, err := e.GetVelocity(ctx, team.ID, ws.ID, -1)
	if err != nil {
		t.Errorf("[W-VEL-FLOOR] GetVelocity(cycles=-1) returned an error: %v. A negative reaches this "+
			"method from `?cycles=-1` (intParam falls back only on a parse error), and the floor is "+
			"what keeps it out of `LIMIT $3`", err)
	} else if len(neg) != 5 {
		t.Errorf("[W-VEL-FLOOR] a request for -1 cycles returned %d rows, want the 5-cycle default", len(neg))
	}
}
