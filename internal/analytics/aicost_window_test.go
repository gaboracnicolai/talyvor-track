package analytics_test

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
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// aicost_window_test.go — THE LEADERBOARD OF AN N-DAY REPORT WAS NOT DRAWN FROM THAT REPORT'S
// COHORT, SO IT COULD SUM TO MORE THAN THE REPORT'S OWN TOTAL.
//
// GetAICostTrends runs five cost sub-queries. FOUR of them window on
// `updated_at > NOW() - (INTERVAL '1 day' * $2::int)`. The fifth — top_cost_issues — took the
// workspace id ALONE:
//
//	SELECT id, identifier, title, ai_cost_usd, ai_tokens
//	  FROM issues WHERE workspace_id = $1 AND ai_cost_usd > 0
//	 ORDER BY ai_cost_usd DESC LIMIT 10
//
// so the leaderboard was all-time while every figure beside it was for the period asked for.
//
// ⚠ THE CONSUMER IS THE AGENT SURFACE, NOT THE PAGE, AND THE CENSUS IS EXACT RATHER THAN
// SUPERLATIVE. frontend/src references top_cost_issues in exactly ONE place — api/types.ts:430,
// which DECLARES it — and in zero components: the only file that touches an AICostTrends value is
// AICostChart.tsx, which reads daily_costs (:23), total_cost_usd and avg_cost_per_issue (:36) and
// projected_monthly_usd (:41), and nothing else. So the field is declared and never rendered; the
// only consumer of its VALUE is mcp.Server.toolGetAICosts (internal/mcp/server.go:1028), which
// trims it to five and
// returns it in a payload it stamps `"period_days": in.Days`, under a published tool description
// that reads "Workspace LLM cost breakdown FOR THE LAST N DAYS (default 7) … total spend, top-5
// most expensive issues, and projected monthly spend" (server.go:385). The tool exists so an agent
// can answer "should I run another duplicate detection pass this week?" with a real budget check;
// it was answering with this week's total beside an all-time leaderboard.
//
// ⚠ MEASURED ON REAL POSTGRES THROUGH THE SHIPPED ROUTE, credited through the SHIPPED WRITER
// (issue.Store.RecordRequestSpendAttributed, the syncer's live accumulator), days=7:
//
//	total_cost_usd  $101.00
//	top_cost_issues T2-1 $101.00 · T2-2 $50.00   → the leaderboard sums to $151.00
//
// A report whose own leaderboard exceeds its own total is wrong under either reading of what the
// total means, which is why this is a fix and not a preference.
//
// ⚠⚠ WHAT THIS FILE DOES NOT CLAIM, AND IT IS THE BIGGER NUMBER. Windowing the leaderboard makes
// the report SELF-CONSISTENT; it does not make it TRUE. All five sub-queries read
// issues.ai_cost_usd, which is a LIFETIME running total per issue, and bucket it by
// issues.updated_at, which is the row's LAST TOUCH. ai_spend_events — the ledger migration 0006
// added "to track WHEN cost arrived", carrying its own created_at and an index on
// (workspace_id, created_at DESC) — is read by ZERO queries in this repo outside
// issue.Store.UnattributedSpend, which is itself unwindowed. Measured on the same fixture as
// below: ledger spend in the last 7 days $1.00, the report's answer $101.00, projected monthly
// $432.86 against a truth of $4.29. That is a decision about what total_cost_usd MEANS and it is
// written up in the queue with its numbers, not merged here.
//
// ⚠ THE COHORT IS OTHERWISE UNTOUCHED: this file changes WHICH ROWS REACH THE LEADERBOARD to the
// rows the other four sub-queries already use, and nothing about the window predicate itself or
// maxWindowDays — the product decisions #93 wrote up.

const (
	// A's old spend and B's spend are both outside the 7-day report and inside the 365-day one,
	// by margins no clock skew can close. Computed, never written down as literals.
	aicostOldSpendDaysAgo = 400 // A: outside BOTH windows
	aicostMidSpendDaysAgo = 200 // B: outside 7, inside 365 — the row the leaderboard leaked
)

// aicostSpender credits `usd` to `iss` through the SHIPPED writer and then ages BOTH the ledger
// row and the issue row by daysAgo.
//
// ⚠ AGEING BOTH IS THE POINT AND IS NOT A CONVENIENCE. The credit sets ai_spend_events.created_at
// and issues.updated_at to NOW() in the same statement (issue/store.go:1069), and neither moves
// again on its own, so "a spend that happened N days ago and was never touched since" IS both
// columns N days back. Ageing only the ledger would leave updated_at at NOW(), the row would sit
// inside every window, and the assertions below would pass for a reason unrelated to the finding.
func aicostSpender(t *testing.T, d *testutil.DB, store *issue.Store, wsID string, iss *model.Issue,
	usd float64, tokens, daysAgo int, reqID string) {
	t.Helper()
	ctx := context.Background()
	resolved, landed, err := store.RecordRequestSpendAttributed(ctx, reqID, "", iss.Identifier, usd, tokens, wsID)
	if err != nil {
		t.Fatalf("shipped spend writer: %v", err)
	}
	if !resolved || !landed {
		t.Fatalf("shipped spend writer did not credit %s: resolved=%v landed=%v — the fixture never "+
			"landed and nothing below is about a credited issue", iss.Identifier, resolved, landed)
	}
	if daysAgo <= 0 {
		return
	}
	age := fmt.Sprintf("%d days", daysAgo)
	if _, err := d.Pool.Exec(ctx,
		`UPDATE ai_spend_events SET created_at = NOW() - $2::interval WHERE request_id = $1`, reqID, age); err != nil {
		t.Fatalf("age ledger row: %v", err)
	}
	if _, err := d.Pool.Exec(ctx,
		`UPDATE issues SET updated_at = NOW() - $2::interval WHERE id = $1`, iss.ID, age); err != nil {
		t.Fatalf("age issue row: %v", err)
	}
}

// aicostReport GETs the SHIPPED route and decodes the two fields under test plus the cohort key.
func aicostReport(t *testing.T, d *testutil.DB, wsID string, days int) (total float64, top []struct {
	IssueID    string  `json:"issue_id"`
	Identifier string  `json:"identifier"`
	CostUSD    float64 `json:"cost_usd"`
}, body string) {
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
	body = rr.Body.String()
	var out struct {
		Total float64 `json:"total_cost_usd"`
		Top   []struct {
			IssueID    string  `json:"issue_id"`
			Identifier string  `json:"identifier"`
			CostUSD    float64 `json:"cost_usd"`
		} `json:"top_cost_issues"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode ai-costs report: %v (body %s)", err, body)
	}
	return out.Total, out.Top, body
}

// aicostFixture seeds the workspace both tests below share and returns (workspace, A, B).
//
//	A — $100 credited 400 days ago, then $1 credited NOW. Last touched NOW.
//	B — $50 credited 200 days ago. Never touched since.
func aicostFixture(t *testing.T, d *testutil.DB) (wsID string, a, b *model.Issue) {
	t.Helper()
	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)
	store := issue.NewStore(d.Pool)
	a = d.Issue(t, ws.ID, tm.ID)
	b = d.Issue(t, ws.ID, tm.ID)
	aicostSpender(t, d, store, ws.ID, a, 100, 100000, aicostOldSpendDaysAgo, "req-"+a.ID+"-old")
	aicostSpender(t, d, store, ws.ID, b, 50, 50000, aicostMidSpendDaysAgo, "req-"+b.ID+"-mid")
	aicostSpender(t, d, store, ws.ID, a, 1, 1000, 0, "req-"+a.ID+"-new")
	return ws.ID, a, b
}

// aicostAgeDays reads how long ago a row was last touched, straight from the column the report
// windows on. The premises below are read out of the table rather than derived from the constants
// above, so a fixture that silently failed to age cannot make this file pass for the wrong reason.
func aicostAgeDays(t *testing.T, d *testutil.DB, issueID string) float64 {
	t.Helper()
	var u time.Time
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT updated_at FROM issues WHERE id = $1`, issueID).Scan(&u); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	return time.Since(u).Hours() / 24
}

// TestAICostReport_TheLeaderboardIsDrawnFromTheReportsOwnCohort.
//
// THE PREMISE ASSERTIONS ARE LOAD-BEARING AND COME FIRST. The vacuity modes are "no issue carries
// cost" (then the leaderboard is empty and every assertion holds for nothing) and "B is inside the
// 7-day window after all" (then there is no out-of-cohort row to leak and the same is true).
func TestAICostReport_TheLeaderboardIsDrawnFromTheReportsOwnCohort(t *testing.T) {
	d := testutil.New(t)
	wsID, a, b := aicostFixture(t, d)

	// PREMISE 1: both issues really carry cost, so both are eligible for a leaderboard that
	// filters on ai_cost_usd > 0.
	for _, iss := range []*model.Issue{a, b} {
		var cost float64
		if err := d.Pool.QueryRow(context.Background(),
			`SELECT ai_cost_usd FROM issues WHERE id = $1`, iss.ID).Scan(&cost); err != nil {
			t.Fatalf("read ai_cost_usd: %v", err)
		}
		if cost <= 0 {
			t.Fatalf("PREMISE 1: %s carries ai_cost_usd = %v, want > 0 — the fixture never "+
				"credited it and the leaderboard is empty for a reason unrelated to the window",
				iss.Identifier, cost)
		}
	}

	// PREMISE 2: A is inside the 7-day cohort and B is outside it. Without this the two rows are
	// indistinguishable to the report and nothing below can fail.
	if age := aicostAgeDays(t, d, a.ID); age > 7 {
		t.Fatalf("PREMISE 2: A was last touched %.1f days ago, which is OUTSIDE the 7-day window — "+
			"the report has no in-cohort row and this test proves nothing", age)
	}
	if age := aicostAgeDays(t, d, b.ID); age <= 7 {
		t.Fatalf("PREMISE 2: B was last touched %.1f days ago, which is INSIDE the 7-day window — "+
			"there is no out-of-cohort row for the leaderboard to leak and this test proves nothing", age)
	}

	total, top, body := aicostReport(t, d, wsID, 7)

	// (a) No leaderboard row may name an issue outside the cohort the rest of the report used.
	var sum float64
	for _, row := range top {
		sum += row.CostUSD
		if age := aicostAgeDays(t, d, row.IssueID); age > 7 {
			t.Errorf("the 7-day ai-cost report's leaderboard names %s, last touched %.0f days ago — "+
				"outside the window every other figure in the same payload was computed over. "+
				"mcp.Server.toolGetAICosts returns this list under \"period_days\": 7. Body: %s",
				row.Identifier, age, body)
		}
	}

	// (b) The consequence a reader can see without knowing the schema: the leaderboard cannot sum
	// to more than the total it is printed beside. It is a strict subset of the same cohort, so
	// this holds for every workspace and every window — it is a property, not this fixture's
	// arithmetic.
	if sum > total {
		t.Errorf("the 7-day ai-cost report's leaderboard sums to $%.2f inside a report whose own "+
			"total_cost_usd is $%.2f. Body: %s", sum, total, body)
	}
}

// TestAICostReport_AWiderWindowStillReachesTheOlderSpender is the companion that makes the test
// above non-vacuous, and it is the reason a fix cannot simply empty the leaderboard.
//
// ⚠ THIS IS THE ASSERTION A NARROWING BREAKS. "No row outside the window" is satisfied perfectly
// by returning nothing at all; only a test that demands the RIGHT rows back can tell a window from
// a deletion. B is outside 7 days and inside 365, so it must be absent from the first report and
// present in this one — the same row, the same fixture, decided by the window alone.
func TestAICostReport_AWiderWindowStillReachesTheOlderSpender(t *testing.T) {
	d := testutil.New(t)
	wsID, a, b := aicostFixture(t, d)

	// PREMISE: B is inside the 365-day window. aicostMidSpendDaysAgo is 200, but the premise is
	// read from the column rather than from the constant.
	if age := aicostAgeDays(t, d, b.ID); age > 365 {
		t.Fatalf("PREMISE: B was last touched %.1f days ago, OUTSIDE the 365-day cap — it cannot "+
			"appear in any report a caller is allowed to ask for and this test proves nothing", age)
	}

	_, top, body := aicostReport(t, d, wsID, 365)
	got := map[string]bool{}
	for _, row := range top {
		got[row.Identifier] = true
	}
	for _, want := range []*model.Issue{a, b} {
		if !got[want.Identifier] {
			t.Errorf("the 365-day ai-cost report's leaderboard is missing %s, which carries cost "+
				"and was last touched inside that window — the leaderboard was narrowed rather "+
				"than windowed. Body: %s", want.Identifier, body)
		}
	}
}
