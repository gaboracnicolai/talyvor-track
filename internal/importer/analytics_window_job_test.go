package importer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// analytics_window_job_test.go — THE REPORT EVERY DATE MERGE ON THIS ITEM WAS JUSTIFIED THROUGH,
// DRIVEN WITH DATES A REAL EXPORT ACTUALLY CARRIES.
//
// #83 landed the provider's `created_at` because analytics computes
// `EXTRACT(EPOCH FROM completed_at - created_at)` and a defaulted created_at made it NEGATIVE;
// #84 landed it on the API transports, #85 landed `updated_at`, and every one of those merges
// verified itself through analytics.GetTimeToResolution. Every one used a fixture dated 200 days
// ago — and jira_csv_created_job_test.go:31 states in writing WHY:
//
//	"both analytics queries filter `created_at > NOW() - INTERVAL '1 day' * $2` with $2 clamped
//	 to 365, so a hardcoded date would silently age out of the window"
//
// THE WINDOW WAS KNOWN AND WAS DESIGNED AROUND. What was never asked is where a REAL export's
// issues land relative to it. MEASURED on the same real Jira Cloud project this item has now
// measured five times (hibernate.atlassian.net, project HHH, anonymous, whole-population counts
// from POST /rest/api/3/search/approximate-count — scripts/w34-analytics-window-probe.py):
//
//	issues in the project                                             20,550
//	  ... resolved (resolutiondate IS NOT EMPTY)                      18,267
//	  ... AND created within the 365-day cap — i.e. the only ones
//	      GetTimeToResolution can ever see, at the widest window
//	      a caller is ALLOWED to ask for                                 756   (4.1%)
//	  ... older than the cap: real rows, real completion times,
//	      correctly imported, unreachable by that report at any
//	      window                                                     17,511  (95.9%)
//
// AND AN IMPORT IS THE ONE THING THAT PRODUCES SUCH ROWS. A native Track issue is created in
// Track, so it is young by construction; only an import writes a created_at from years ago. The
// 4.1% above is a LIVE project's figure — a MIGRATED one stops receiving new issues, so its
// visible share decays to zero as the migration ages, and the report then measures nothing at all.
//
// ⚠ WHAT THIS FILE DOES AND DOES NOT CLAIM. That the window is on created_at, and that it is
// capped at 365 days, are PRODUCT DECISIONS with numbers attached; they are written up in the
// queue and NOT changed here. What is a defect, and what this pins, is that the report answers an
// empty cohort with `0` in every field and says nothing about the cohort: a workspace holding
// 18,267 imported resolved issues and a workspace holding NO ISSUES AT ALL produce byte-identical
// resolution reports. A zero that was computed and a zero that stands for "nothing was measured"
// are the same four bytes.
//
// The two imports below are IDENTICAL IN CONTENT — same title, same status, same resolution, same
// true cycle time of exactly 2400 hours. They differ in ONE respect: how long ago the dates are.
// That is what makes the two reports comparable: any difference between them is the age of the
// row and nothing else.

const (
	// The measured Jira CSV export layout, HARDCODED rather than read from the package constant
	// (#75's C6: an assertion that formats with the same constant the code parses with compares a
	// constant to itself and passes for every possible value).
	windowTestLayout = "2/Jan/2006 3:04 PM"

	// INSIDE the cap: opened 200 days ago, finished 100 days ago.
	windowInsideCreatedDaysAgo  = 200
	windowInsideResolvedDaysAgo = 100

	// OUTSIDE the cap: opened 800 days ago, finished 700 days ago. The SAME 100-day cycle time.
	// 800 > 365 by a margin no clock skew or leap second can close, and the real median created-age
	// on the instance measured above is far older still.
	windowOutsideCreatedDaysAgo  = 800
	windowOutsideResolvedDaysAgo = 700

	// Both fixtures resolve in exactly 100 days. The report over the INSIDE workspace must produce
	// this number; the OUTSIDE workspace holds a row with the identical true value and produces 0.
	windowTrueCycleHours = float64(100 * 24)
)

// windowFixture renders a one-row Jira CSV opened `createdDaysAgo` ago and resolved
// `resolvedDaysAgo` ago. Dates are COMPUTED, never written down: a literal would age relative to
// the window and this test would stop testing anything while staying green — the same trap
// jira_csv_created_job_test.go names.
func windowFixture(createdDaysAgo, resolvedDaysAgo int) string {
	now := time.Now().UTC()
	// Truncated to the minute because the layout carries no seconds; a round-trip would otherwise
	// lose them and fail an assertion for a reason unrelated to the finding.
	created := now.Add(-time.Duration(createdDaysAgo) * 24 * time.Hour).Truncate(time.Minute)
	resolved := now.Add(-time.Duration(resolvedDaysAgo) * 24 * time.Hour).Truncate(time.Minute)
	return "Summary,Description,Status,Priority,Resolution,Created,Resolved\n" +
		fmt.Sprintf("Imported from a real backlog,d,Closed,High,Fixed,%s,%s\n",
			created.Format(windowTestLayout), resolved.Format(windowTestLayout))
}

// runWindowImport drives the SHIPPED async path end to end on real Postgres: a job row + payload,
// claimed and executed by the real runner, writing through the real issue.Store.
func runWindowImport(t *testing.T, d *testutil.DB, body string) (wsID string) {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	if _, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return ws.ID
}

// resolutionReport GETs the SHIPPED route (analytics.Handler.Resolution, the authorized surface a
// member actually reaches) and returns the decoded body as a map — deliberately NOT as
// analytics.ResolutionStats. Decoding into the struct would answer "what does the struct hold",
// and the question here is "what does a CLIENT receive": a field that does not exist in the JSON
// is invisible to a struct decode (it silently zeroes) and plainly absent from a map.
func resolutionReport(t *testing.T, d *testutil.DB, wsID string, days int) map[string]any {
	t.Helper()
	h := analytics.NewHandler(analytics.New(d.Pool))
	r := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/v1/workspaces/%s/analytics/resolution?days=%d", wsID, days), nil)
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", authz.RoleMember))
	rr := httptest.NewRecorder()
	h.Resolution(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("resolution report: HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode resolution report: %v (body %s)", err, rr.Body.String())
	}
	return out
}

func num(t *testing.T, report map[string]any, key string) float64 {
	t.Helper()
	v, ok := report[key]
	if !ok {
		t.Fatalf("the resolution report carries no %q field. Body: %v", key, report)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%q = %v (%T), want a number", key, v, v)
	}
	return f
}

// TestResolutionReport_AnImportedBacklogOlderThanTheWindowIsNotAMeasuredZero.
//
// THE PREMISE ASSERTIONS ARE LOAD-BEARING AND COME FIRST. This test's vacuity mode is NOT "the
// fixture was empty" — it is "the import never landed", which would make every zero below true for
// the wrong reason. So the out-of-window row is read straight out of the issues table first: it
// must exist, carry the PROVIDER's created_at (not the import instant, which would put it INSIDE
// the window and quietly re-test #83 instead of this) and a non-null completed_at.
func TestResolutionReport_AnImportedBacklogOlderThanTheWindowIsNotAMeasuredZero(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	outsideWS := runWindowImport(t, d,
		windowFixture(windowOutsideCreatedDaysAgo, windowOutsideResolvedDaysAgo))

	// PREMISE 1: the row landed, with the provider's dates and a real completion time.
	var (
		created, completed time.Time
		rows               int
	)
	if err := d.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issues WHERE workspace_id = $1 AND completed_at IS NOT NULL`,
		outsideWS).Scan(&rows); err != nil {
		t.Fatalf("count imported rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("PREMISE FAILED: %d imported rows with a completion time, want 1 — the import did "+
			"not land, so every zero below would be true for the wrong reason", rows)
	}
	if err := d.Pool.QueryRow(ctx,
		`SELECT created_at, completed_at FROM issues WHERE workspace_id = $1`,
		outsideWS).Scan(&created, &completed); err != nil {
		t.Fatalf("read dates: %v", err)
	}
	ageDays := time.Since(created).Hours() / 24
	if ageDays < 400 {
		t.Fatalf("PREMISE FAILED: imported created_at is %.0f days old, want ≈%d — a defaulted "+
			"created_at would put the row INSIDE the window and this test would measure #83 again",
			ageDays, windowOutsideCreatedDaysAgo)
	}
	if cyc := completed.Sub(created).Hours(); cyc < windowTrueCycleHours-24 || cyc > windowTrueCycleHours+24 {
		t.Fatalf("PREMISE FAILED: the imported row's TRUE cycle time is %.1f h, want ≈%.0f", cyc, windowTrueCycleHours)
	}

	// PREMISE 2 — THE COMPANION THAT MAKES THE ZERO UNFAKEABLE. A byte-identical import whose only
	// difference is that its dates are recent must produce a live, correct number from the same
	// report. Without this, every assertion below would also pass on an importer that writes
	// nothing at all, on a broken CSV layout, or on an analytics engine that always answers zero.
	insideWS := runWindowImport(t, d,
		windowFixture(windowInsideCreatedDaysAgo, windowInsideResolvedDaysAgo))
	inside := resolutionReport(t, d, insideWS, 365)
	if got := num(t, inside, "median_hours"); got < windowTrueCycleHours-24 || got > windowTrueCycleHours+24 {
		t.Fatalf("PREMISE FAILED: an IN-window import reports median_hours = %.1f, want ≈%.0f — "+
			"the report is not alive, so a zero from the out-of-window workspace proves nothing",
			got, windowTrueCycleHours)
	}
	if got := num(t, inside, "sample_size"); got != 1 {
		t.Errorf("an IN-window workspace holding exactly 1 resolved issue reports sample_size = %.0f, want 1", got)
	}

	// THE FINDING, HALF ONE — pinned as CURRENT, DELIBERATE behaviour, not asserted as a defect:
	// the out-of-window row is invisible to the report at the widest window a caller may request.
	// The window predicate and the 365-day cap are product decisions and this merge changes
	// neither; this line exists so that if either is ever changed, the change is loud.
	outside := resolutionReport(t, d, outsideWS, 100000) // clamped to maxWindowDays
	if got := num(t, outside, "median_hours"); got != 0 {
		t.Errorf("median_hours = %.1f for a workspace whose only resolved issue was created %d days "+
			"ago; the shipped window is created_at-based and capped, so 0 is expected here. If this "+
			"changed, the window semantics changed — update this test and the queue entry together.",
			got, windowOutsideCreatedDaysAgo)
	}

	// THE FINDING, HALF TWO — THE DEFECT. That zero is indistinguishable from a measured one. A
	// workspace with NO ISSUES AT ALL must not produce the same report as a workspace holding a
	// real, correctly-imported, resolved backlog. The cohort size is the only thing that can say
	// so: a client reading `median_hours: 0` cannot tell "these issues resolve instantly" from
	// "nothing was measured", and today the API gives it nothing else to read.
	empty := d.Workspace(t)
	emptyReport := resolutionReport(t, d, empty.ID, 365)
	if num(t, outside, "sample_size") != 0 {
		t.Errorf("sample_size = %v for a workspace whose issues are all outside the window, want 0",
			outside["sample_size"])
	}
	if num(t, emptyReport, "sample_size") != 0 {
		t.Errorf("sample_size = %v for a workspace with no issues, want 0", emptyReport["sample_size"])
	}
	// And the sample size must be the thing that separates a real measurement from an empty one:
	// same zero median, different cohort — that is the whole content of this merge.
	if num(t, inside, "sample_size") == num(t, emptyReport, "sample_size") {
		t.Errorf("a workspace with a measured cohort and a workspace with none report the SAME "+
			"sample_size (%v) — the field does not distinguish them", inside["sample_size"])
	}
}
