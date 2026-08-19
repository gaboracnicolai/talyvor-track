package analytics_test

// "THE DEFAULT ANALYTICS WINDOW" IS WRITTEN DOWN SIX TIMES AND GUARDED ZERO TIMES, AND THE TWO
// SOURCES ANSWER TWO DIFFERENT ROUTES OF THE SAME QUESTION.
//
// A caller who omits `days` gets the HANDLER's literal: `intParam(r, "days", 30)`, five separate
// call sites (handler.go:109, :128, :142, :233, :242), each carrying its own `30`. A caller who
// sends `days=0` — or `days=abc`, which `intParam` also folds to its fallback — reaches the
// ENGINE's `defaultWindowDays = 30` through clampDays (engine.go:90, wired at :308, :408, :510).
// Six numbers, one meaning, and nothing ties them together.
//
// MEASURED at bfc5574 over the WHOLE repository (`go test ./...`) against real Postgres, membership
// by SET SUBTRACTION against the run's own baseline FAIL set (scripts/w34-default-window-controls-5b91.py):
//
//	N1  defaultWindowDays 30 -> 90                      NOT CAUGHT — by anything, anywhere
//	C4  maxWindowDays 365 -> 3650 (measured under #166)  CAUGHT, by the window-clamp wiring tests
//
// The CAP is guarded. The DEFAULT is not. The asymmetry is why this is a hole and not a taste
// argument: #165 wired the ceiling and nobody wired the floor beside it.
//
// ⚠ THIS CORRECTS A HANDOVER NOTE I WROTE MYSELF ONE MERGE EARLIER, AND THE CORRECTION IS THE
// USEFUL PART. #166's handover called `TestClampDays_BoundsRespected` (engine_test.go:298) "a guard
// that cannot fail". MEASURED, THAT IS TOO STRONG AND THE PRECISE STATEMENT IS NARROWER: it catches
// three of the five mutations put to it — `days <= 0` weakened to `days < 0`, the default replaced
// by a literal, and the cap raised inside the function (which it catches ALONE). What it cannot see
// is a move in either CONSTANT, because it compares clampDays' output to the very constants
// clampDays returns. It is blind to its own constants, not inert.
//
// WHAT THIS FILE PINS, AND WHY IT IS ONE FILE RATHER THAN TWO: the two defaults are asserted through
// the SAME route against the SAME fixture, so they cannot be repaired independently into
// disagreement. A cohort seeded at 10 / 45 / 200 days old distinguishes a 30-day window from a
// 90-day one and from an unbounded one; a fixture whose rows all sit inside every candidate window
// would pass whatever the number was.
//
// ⚠ WHAT IS NOT DECIDED HERE: whether 30 is the right default is a product number and is left
// exactly where it is. This file pins that the two paths agree on it and that a change to either is
// visible — not that the value is correct.
//
// THE CONTROLS, 6/6, each predicted BEFORE the run, each over the whole repository:
//
//	D1  defaultWindowDays 30 -> 90            CAUGHT by this file and by NOTHING else
//	D2  D1 with this file deleted             NOT CAUGHT — the measured blindness on main
//	D3  the ROUTE's own literal 30 -> 90      CAUGHT by this file (the engine never sees that number)
//	D4  clampDays' `days <= 0` -> `days < 0`  CAUGHT by the pre-existing unit test AND the wiring
//	                                          tests AND this file — three catchers, so it justifies
//	                                          nothing on its own and is recorded, not claimed
//	D5  `days <= 0` -> `days < 1` (VOID)      NOT CAUGHT
//	D6  the window predicate dropped          CAUGHT by this file's anti-vacuity halves and by two
//	                                          pre-existing guards
//
// ⚠ D1 AND D3 ARE THE PAIR THAT MATTERS: they move the two ends of the same product number, they
// are in different files, and before this test NEITHER of them reddened anything.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
)

// distributionGet drives Handler.Distribution as an authorized member, which is the only way the
// handler's own `30` is observable — the engine never sees it.
func distributionGet(t *testing.T, h *analytics.Handler, wsID, query string) []analytics.DistributionBucket {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+wsID+"/analytics/distribution?"+query, nil)
	req = req.WithContext(authz.WithAuthorized(req.Context(), wsID, "member-1"))
	rec := httptest.NewRecorder()
	h.Distribution(rec, req)
	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("distribution?%s: status %d, body %q", query, rec.Result().StatusCode, rec.Body.String())
	}
	var out []analytics.DistributionBucket
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("distribution?%s: body is not a bucket list: %v (%q)", query, err, rec.Body.String())
	}
	return out
}

func totalCount(buckets []analytics.DistributionBucket) int {
	n := 0
	for _, b := range buckets {
		n += b.Count
	}
	return n
}

func TestAnalytics_TheDefaultWindowIsOneNumberOnBothPathsIntoIt_RealPG(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	h := analytics.NewHandler(analytics.New(d.Pool))

	// 10 days old: inside every candidate window. 45: inside 90 but OUTSIDE 30 — the row that can
	// tell 30 from 90. 200: outside both, inside the 365 cap — the row that separates "the default"
	// from "no window at all".
	seedDistIssue(t, d, ws.ID, team.ID, 1, "todo", "NOW() - INTERVAL '10 days'", "NOW() - INTERVAL '10 days'", nil, []string{}, 0)
	seedDistIssue(t, d, ws.ID, team.ID, 2, "todo", "NOW() - INTERVAL '45 days'", "NOW() - INTERVAL '45 days'", nil, []string{}, 0)
	seedDistIssue(t, d, ws.ID, team.ID, 3, "todo", "NOW() - INTERVAL '200 days'", "NOW() - INTERVAL '200 days'", nil, []string{}, 0)

	// ── PATH 1: `days` omitted. This is the handler's literal 30, and the engine never sees it.
	omitted := totalCount(distributionGet(t, h, ws.ID, "group_by=status"))
	if omitted != 1 {
		t.Errorf("days omitted: cohort of %d, want 1 (the 10-day-old issue only). "+
			"The route's default window is not 30 days — 2 would mean ~90, 3 would mean no window. "+
			"handler.go supplies this default as a bare literal at five call sites.", omitted)
	}

	// ── PATH 2: `days=0`. intParam passes 0 through; clampDays substitutes defaultWindowDays.
	zero := totalCount(distributionGet(t, h, ws.ID, "group_by=status&days=0"))
	if zero != 1 {
		t.Errorf("days=0: cohort of %d, want 1. This path reaches defaultWindowDays through "+
			"clampDays, not the handler's literal.", zero)
	}

	// ── THE TIE. The two are separate numbers in separate files; this is the only assertion that
	// fails when they drift apart, which is the defect the count assertions above cannot express.
	if omitted != zero {
		t.Errorf("the two defaults DISAGREE: `days` omitted serves a %d-issue cohort and `days=0` "+
			"serves a %d-issue cohort over the identical fixture. handler.go's literal and "+
			"engine.go's defaultWindowDays are the same product number written down twice.",
			omitted, zero)
	}

	// ── ANTI-VACUITY. "Always answer 1" would satisfy everything above. An explicit, LEGAL window
	// wider than the default must widen the cohort, and one wider than the cap must not exceed it.
	if got := totalCount(distributionGet(t, h, ws.ID, "group_by=status&days=90")); got != 2 {
		t.Errorf("days=90: cohort of %d, want 2 — the fixture's 45-day-old issue must join. "+
			"Without this the assertions above are satisfied by a report that ignores `days`.", got)
	}
	if got := totalCount(distributionGet(t, h, ws.ID, "group_by=status&days=100000")); got != 3 {
		t.Errorf("days=100000: cohort of %d, want 3 — clamped to maxWindowDays (365), which still "+
			"contains every seeded row. This is the companion that keeps the default assertions "+
			"from being satisfied by a window that is always small.", got)
	}
}
