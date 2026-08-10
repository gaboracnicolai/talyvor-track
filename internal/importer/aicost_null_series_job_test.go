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
	"github.com/talyvor/track/internal/testutil"
)

// aicost_null_series_job_test.go — THE AI-COST REPORT ANSWERS A MIGRATED BACKLOG WITH `null` WHERE
// ITS ONLY CLIENT MAPS, AND THE SHIPPED CHART THROWS ON IT.
//
// #93 asked what the resolution report says about an imported backlog older than the analytics
// window and found a zero that was never measured. THE SAME QUESTION PUT TO THE AI-COST REPORT HAS
// A DIFFERENT AND WORSE ANSWER: it does not return a wrong number, it returns a shape the client
// cannot consume. MEASURED through the SHIPPED route on real Postgres, for a workspace whose only
// content is a correctly imported backlog opened 800 days ago and last touched 600 days ago:
//
//	{"total_cost_usd":0,"daily_costs":null,"top_cost_issues":null,
//	 "cost_by_team":null,"cost_by_label":null,"projected_monthly_usd":0,"avg_cost_per_issue":0}
//
// ALL FOUR ARRAY FIELDS ARE `null`. Go marshals a nil slice as `null`, and GetAICostTrends leaves
// every one of them nil when its query returns no rows.
//
// ⚠ THE CLIENT CANNOT SURVIVE IT, AND THAT IS MEASURED RATHER THAN INFERRED.
// frontend/src/components/analytics/AICostChart.tsx:23 is `trends.daily_costs.map(...)`, unguarded.
// Run against the body above:
//
//	TypeError: Cannot read properties of null (reading 'map')
//
// The Analytics page renders that chart unconditionally (`trends.data ? <AICostChart …/>` — the
// object IS truthy, only the field inside it is null) and there is no ErrorBoundary anywhere in
// frontend/src, so the page does not degrade, it goes.
//
// ⚠⚠ AND NOTHING IN THE REPO CAN SEE IT. frontend/src/api/types.ts:429 declares
// `daily_costs: DailyCost[]` — NON-NULLABLE — so TypeScript believes this state is unreachable and
// `npm run typecheck` (the whole of what the `frontend` CI job runs, alongside `build`; there is no
// test runner in that package at all) is green with the crash present. On the Go side a decode into
// analytics.AICostTrends is equally blind: `null` unmarshals into a nil slice in silence, exactly
// as `[]` does. That is why the assertions below read the RAW BYTES of each field out of a
// map[string]json.RawMessage — the same argument #93's file makes for reading a map rather than a
// struct, one level down: there the field was absent, here it is present and its VALUE is the loss.
//
// ⚠ THE OTHER THREE ANALYTICS ENDPOINTS ALREADY ANSWER `[]`, so this is a divergence inside one
// file rather than a taste. analytics/handler.go coerces in Velocity (`out = []CycleVelocity{}`),
// Distribution (`[]DistributionBucket{}`) and Workload (`[]MemberWorkload{}`); AICosts is the one
// handler with no coercion, and its fields are nested so a top-level coercion could not express it.
// The SECOND consumer already defends itself by hand for the one field it uses:
// mcp/server.go:1032 builds `make([]map[string]any, 0, len(top))` for top_cost_issues. Somebody has
// met this before, in the caller rather than in the engine, and only for a quarter of the surface —
// which is why the fix is in GetAICostTrends and covers both consumers and all four fields.
//
// ⚠ WHAT IS AND IS NOT CLAIMED ABOUT THE TRIGGER. The state is "no issue in this workspace has
// updated_at inside the window", and three things reach it: an EMPTY workspace (pinned below), a
// workspace idle longer than the window, and A WORKSPACE FULL OF CORRECTLY IMPORTED WORK. Only the
// third is this item's, and it is the surprising one — a native issue is written now, so its
// updated_at is always inside the window; only an import writes one from years ago, and a MIGRATED
// project stops receiving updates altogether. The default the page actually requests is
// `days=30` (Analytics.tsx:12), which is narrower still than the 365 asserted here.
//
// ⚠ THE WINDOW ITSELF IS UNTOUCHED. Whether the AI-cost window should key on updated_at, and
// whether 365 is the right cap, are the product decisions #93 wrote up with numbers attached. This
// file changes the SHAPE of the answer, never the cohort.

// aiCostNullSeriesLayout is the measured Jira CSV export layout, HARDCODED rather than read from
// the package constant — #75's C6: an assertion that formats with the same constant the code parses
// with compares a constant to itself and passes for every possible value.
const aiCostNullSeriesLayout = "2/Jan/2006 3:04 PM"

const (
	// A migrated backlog: opened 800 days ago, finished 700, LAST TOUCHED 600. All three are
	// outside maxWindowDays (365) by a margin no clock skew or leap second can close, and the real
	// median created-age on the instance #93 measured is far older still.
	aiCostMigratedCreatedDaysAgo  = 800
	aiCostMigratedResolvedDaysAgo = 700
	aiCostMigratedUpdatedDaysAgo  = 600
)

// aiCostMigratedFixture renders a one-row Jira CSV export for a migrated project.
//
// ⚠ IT CARRIES ITS OWN `Updated` COLUMN AND DOES NOT REUSE windowFixture, WHICH HAS NONE. That
// omission is not cosmetic: without the column issues.updated_at takes its DEFAULT NOW(), the row
// lands INSIDE the window whatever its Created says, and every assertion below would pass for the
// wrong reason. This was measured on the first run of the probe behind this file — the premise
// printed `updated 0d ago` — which is why PREMISE 2 exists and why C4 blinds this column.
//
// Dates are COMPUTED, never written down: a literal ages relative to the window and the test stops
// testing anything while staying green — the trap jira_csv_created_job_test.go names.
func aiCostMigratedFixture() string {
	now := time.Now().UTC()
	// Truncated to the minute because the layout carries no seconds; a round trip would otherwise
	// lose them and fail an assertion for a reason unrelated to the finding.
	at := func(daysAgo int) string {
		return now.Add(-time.Duration(daysAgo) * 24 * time.Hour).Truncate(time.Minute).
			Format(aiCostNullSeriesLayout)
	}
	return "Summary,Description,Status,Priority,Resolution,Created,Updated,Resolved\n" +
		fmt.Sprintf("Imported from a migrated backlog,d,Closed,High,Fixed,%s,%s,%s\n",
			at(aiCostMigratedCreatedDaysAgo), at(aiCostMigratedUpdatedDaysAgo),
			at(aiCostMigratedResolvedDaysAgo))
}

// aiCostReportRaw GETs the SHIPPED route (analytics.Handler.AICosts — the authorized surface a
// member actually reaches) and returns the body as field name → RAW JSON BYTES.
//
// Deliberately NOT decoded into analytics.AICostTrends: `null` and `[]` both unmarshal into a nil
// slice, so a struct decode is blind to the entire finding by construction.
func aiCostReportRaw(t *testing.T, d *testutil.DB, wsID string, days int) (map[string]json.RawMessage, string) {
	t.Helper()
	h := analytics.NewHandler(analytics.New(d.Pool))
	r := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/v1/workspaces/%s/analytics/ai-costs?days=%d", wsID, days), nil)
	r = r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", authz.RoleMember))
	rr := httptest.NewRecorder()
	h.AICosts(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("ai-costs report: HTTP %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode ai-costs report: %v (body %s)", err, body)
	}
	return out, body
}

// aiCostArrayFields are the four fields frontend/src/api/types.ts declares as non-nullable arrays.
// Listed here rather than derived from the struct: a list derived from the code under test agrees
// with it for every possible value of that code.
var aiCostArrayFields = []string{"daily_costs", "top_cost_issues", "cost_by_team", "cost_by_label"}

// assertArrayNotNull fails when a field is missing, or is present and `null`. The message names the
// client line that dies on it, so a future reader does not have to rediscover why `[]` matters.
func assertArrayNotNull(t *testing.T, report map[string]json.RawMessage, key, body string) {
	t.Helper()
	raw, ok := report[key]
	if !ok {
		t.Fatalf("the ai-cost report carries no %q field at all. Body: %s", key, body)
	}
	if string(raw) == "null" {
		t.Errorf("the ai-cost report answers %q with null, not an array — "+
			"frontend/src/api/types.ts declares it non-nullable and AICostChart.tsx:23 calls "+
			".map() on daily_costs, which throws TypeError on null. Body: %s", key, body)
		return
	}
	if len(raw) == 0 || raw[0] != '[' {
		t.Errorf("the ai-cost report answers %q with %s, want a JSON array. Body: %s", key, raw, body)
	}
}

// TestAICostReport_AMigratedBacklogIsNotAnsweredWithNull.
//
// THE PREMISE ASSERTIONS ARE LOAD-BEARING AND COME FIRST. This test's vacuity modes are "the import
// never landed" (then the workspace is merely empty and this re-tests the second test below) and
// "the row landed INSIDE the window" (then the null never happens and every assertion passes for
// the wrong reason). Both are read straight out of the issues table before the report is called.
func TestAICostReport_AMigratedBacklogIsNotAnsweredWithNull(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	wsID := runWindowImport(t, d, aiCostMigratedFixture())

	// PREMISE 1: the import landed. A zero-row workspace would make the nulls true for a reason
	// that has nothing to do with an imported backlog.
	var landed int
	if err := d.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issues WHERE workspace_id = $1`, wsID).Scan(&landed); err != nil {
		t.Fatalf("count imported rows: %v", err)
	}
	if landed != 1 {
		t.Fatalf("PREMISE 1: imported rows = %d, want 1 — the fixture did not land, so nothing "+
			"below is about an imported backlog", landed)
	}

	// PREMISE 2: the row carries the PROVIDER's updated_at, comfortably outside the widest window
	// a caller is allowed to ask for. Without the `Updated` column this is the import instant and
	// the report would answer with data.
	var updated time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT updated_at FROM issues WHERE workspace_id = $1`, wsID).Scan(&updated); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if ageDays := time.Since(updated).Hours() / 24; ageDays < 366 {
		t.Fatalf("PREMISE 2: the imported row was last touched %.0f days ago, which is INSIDE the "+
			"365-day cap — the report can see it and this test proves nothing", ageDays)
	}

	report, body := aiCostReportRaw(t, d, wsID, 365)
	for _, f := range aiCostArrayFields {
		assertArrayNotNull(t, report, f, body)
	}
}

// TestAICostReport_AnEmptyWorkspaceAnswersLikeItsSiblingEndpoints pins the rule as a divergence
// inside one file rather than as a preference: the same empty workspace, asked the same way, gets
// `[]` from distribution and workload and `null` from ai-costs. The sibling assertions are the
// reference point, so this test states what the file already believes instead of inventing it.
func TestAICostReport_AnEmptyWorkspaceAnswersLikeItsSiblingEndpoints(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)

	// PREMISE: the workspace really is empty — otherwise "no rows in the window" is not the state
	// under test.
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM issues WHERE workspace_id = $1`, ws.ID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("PREMISE: the workspace holds %d issues, want 0", n)
	}

	h := analytics.NewHandler(analytics.New(d.Pool))
	for _, sib := range []struct {
		name string
		fn   http.HandlerFunc
	}{{"distribution", h.Distribution}, {"workload", h.Workload}} {
		r := httptest.NewRequest(http.MethodGet, "/v1/workspaces/x/analytics/"+sib.name, nil)
		r = r.WithContext(authz.WithAuthorizedRole(r.Context(), ws.ID, "m1", authz.RoleMember))
		rr := httptest.NewRecorder()
		sib.fn(rr, r)
		if got := rr.Body.String(); got != "[]\n" {
			t.Fatalf("REFERENCE POINT: %s on an empty workspace answered %q, want \"[]\\n\" — the "+
				"comparison this test rests on has moved", sib.name, got)
		}
	}

	report, body := aiCostReportRaw(t, d, ws.ID, 30)
	for _, f := range aiCostArrayFields {
		assertArrayNotNull(t, report, f, body)
	}
}
