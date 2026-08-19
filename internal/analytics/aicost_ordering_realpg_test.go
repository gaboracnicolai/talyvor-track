package analytics_test

// THE AI-COST REPORT'S TWO *LIMITED* SUB-QUERIES CHOOSE WHICH ROWS SHIP BY AN ORDERING NOTHING
// ASSERTED — SO "THE TOP-5 MOST EXPENSIVE ISSUES" COULD HAVE BEEN FIVE ARBITRARY ISSUES.
//
// `GetAICostTrends` runs five sub-queries. Two of them carry a LIMIT, and in those two the ORDER BY
// is not presentation: it decides MEMBERSHIP.
//
//	top_cost_issues   ORDER BY ai_cost_usd DESC LIMIT 10
//	cost_by_label     ORDER BY SUM(ai_cost_usd) DESC LIMIT 20
//
// MEASURED, NOT INFERRED, at f088be4 — one term at a time, each over the FULL `go test -race
// -count=1 ./...`, each restored in a `finally` with engine.go sha256-verified (harness
// scripts/w34-aicost-ordering-controls-b9d7.py):
//
//	the leaderboard serves the TEN CHEAPEST issues instead   NOT CAUGHT
//	cost_by_label ranks ASC — the cheapest 20 labels         NOT CAUGHT
//
// Both left the whole repository green apart from the 11 pre-existing internal/importer corpus
// failures this machine has with an empty corpus dir.
//
// ⚠ THE ONE THING THAT *DID* RED WAS A QUERY-TEXT FINGERPRINT, AND READING IT AS COVERAGE IS HOW
// THIS SURVIVED. The first spelling of the control — a plain `DESC` -> `ASC` — scored CAUGHT by
// engine_test.go's TestGetAICostTrends_ReturnsDailyCostsAndProjection. That is a pgxmock test which
// FEEDS the leaderboard rows; it cannot see an ordering, and what failed was
// `ExpectQuery("ORDER BY ai_cost_usd DESC LIMIT 10")` no longer matching the statement text. The
// repo has written this lesson down twice already (#152 workload, #153 velocity) and it was still
// the only thing standing here. The measurement above therefore uses a spelling that leaves that
// substring BYTE-IDENTICAL — the real query wrapped in a subselect that takes the cheapest ten —
// so a red can only be an assertion.
//
// ⚠ WHY NO FIXTURE COULD SEE IT: EVERY ONE OF THEM HAS A ONE-ROW COHORT. aicost_window_test.go's
// leaderboard test seeds two cost-carrying issues and its whole subject is that only ONE of them is
// inside the 7-day window; scope_read_test.go's AICosts test seeds one per workspace. With one row
// in the cohort, DESC, ASC and no ORDER BY at all return the same answer. This is #158's class
// again — a fixture whose incidental shape makes a term unfalsifiable — and it is why the fixture
// below seeds TWENTY-ONE cost-carrying issues, more than either LIMIT can hold.
//
// ⚠⚠ AND THE FIXTURE'S OWN PREMISE CAUGHT ME BEFORE THE PRODUCT DID, WHICH IS THE MOST USEFUL
// THING IN THIS FILE. The first spelling seeded the rows cheapest-first and asserted that an
// UNORDERED read would therefore come back in the wrong order — the #158 discipline, applied to my
// own fixture. It failed immediately: `[O-PREMISE]` reported the unordered read returning the most
// expensive issue first. The reason is not the fixture. migrations/0009_analytics.sql creates
//
//	idx_issues_ai_cost ON issues(workspace_id, ai_cost_usd DESC) WHERE ai_cost_usd > 0
//
// and EXPLAIN on the SHIPPED leaderboard statement reports `Index Scan using idx_issues_ai_cost`
// WITH the ORDER BY and WITHOUT it alike — the partial index's second key is already descending, so
// the read is ranked either way and returns [21 20 19 … 12] with no ORDER BY at all.
//
// So the honest scope of this guard is narrower than "the ordering is asserted", and it says so
// rather than letting the next reader assume: DELETING the leaderboard's ORDER BY is INVISIBLE on
// this data shape, by the index rather than by luck. What is caught is ranking the wrong WAY and
// shipping the wrong ROWS — which is what the measurement above found unguarded, and which no index
// can paper over. The ORDER BY is what makes the order a PROMISE rather than a plan artefact; the
// day the planner picks a seq scan it is the only thing left.
//
// ⚠ THE CONTROLS, EACH NAMING ITS PREDICTED VERDICT BEFORE IT RAN, one term at a time, each
// restored in a `finally` with engine.go sha256-verified back to pristine every time
// (scripts/w34-aicost-ordering-controls-b9d7.py — 8 controls, ZERO prediction misses):
//
//	P1  leaderboard serves the TEN CHEAPEST (fingerprint intact)  CAUGHT  [L-MEMBERSHIP][L-PREFIX], ALSO BY NOTHING
//	P2  leaderboard DESC -> ASC (the naive spelling)              CAUGHT  [L-ORDER]+, also the mock's TEXT fingerprint
//	P3  leaderboard's ORDER BY DELETED                            NOT CAUGHT HERE — see the index note above
//	P4  cost_by_label DESC -> ASC                                 CAUGHT  [B-ORDER][B-MEMBERSHIP], ALSO BY NOTHING
//	P5  cost_by_label's ORDER BY DELETED                          CAUGHT  [B-ORDER][B-MEMBERSHIP] — no index ranks a
//	                                                                      GROUP BY over UNNEST, so this half IS visible
//	P6  cost_by_TEAM DESC -> ASC (that one has NO LIMIT)          NOT CAUGHT — must-stay-green, and what stops this
//	                                                                      file reading as "every ORDER BY in the report"
//	P7  whitespace only in the label ORDER BY                     NOT CAUGHT by anything (void control)
//	P8  leaderboard LIMIT 10 -> LIMIT 5                           CAUGHT  [L-LIMIT] alone
//
// P3 and P5 together are the useful pair: the SAME mutation is invisible on the query an index
// happens to rank and visible on the one nothing ranks. Neither result was assumed.
//
// ⚠ THE CONSUMER MAKES THE ORDER A CLAIM ABOUT MONEY RATHER THAN A LAYOUT PREFERENCE.
// frontend/src renders top_cost_issues nowhere (api/types.ts declares it; AICostChart.tsx reads
// daily_costs, total, avg and projection only). Its one consumer of the VALUE is
// mcp.Server.toolGetAICosts, which does `top = top[:5]` — a slice PREFIX, which is "the top five"
// only if the slice is sorted — and publishes it under the tool description "total spend, top-5
// most expensive issues, and projected monthly spend". The tool exists so an agent can answer
// "should I run another duplicate detection pass this week?" with a real budget check.

import (
	"context"
	"fmt"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// aicostRankedIssues is one more than cost_by_label's LIMIT (20) and twice the leaderboard's (10),
// so both sub-queries have to LEAVE SOMETHING OUT and which rows they leave out is observable.
const aicostRankedIssues = 21

// seedRankedCostIssue writes one issue carrying a distinct cost and its own distinct label, last
// touched NOW so it is inside every window this report can be asked for.
func seedRankedCostIssue(t *testing.T, d *testutil.DB, wsID, teamID string, n int, cost float64) {
	t.Helper()
	_, err := d.Pool.Exec(context.Background(), `
        INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
                            creator_id, ai_cost_usd, ai_tokens, labels, created_at, updated_at)
        VALUES ($1, $2, $3::int, 'RANK-' || $3::int, 'ranked ' || $3::int, 'done', 1,
                'rankprobe', $4, 1000, ARRAY['rank-label-' || $3::int], NOW(), NOW())`,
		wsID, teamID, n, cost)
	if err != nil {
		t.Fatalf("seed ranked issue %d (cost %v): %v", n, cost, err)
	}
}

func TestAICostTrends_TheLimitedSubQueriesRankByCost_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	tm := d.Team(t, ws.ID)

	// ── WRITTEN CHEAPEST FIRST so insertion order is the reverse of the ranking, and every cost is a
	// DISTINCT integer so no two rows can be swapped unnoticed and no tie can make an ordering
	// assertion hold for free. (Insertion order turns out not to be what the planner returns — see
	// the index note below — but a fixture whose write order already matched the answer would have
	// been the wrong fixture either way.)
	for i := 1; i <= aicostRankedIssues; i++ {
		seedRankedCostIssue(t, d, ws.ID, tm.ID, i, float64(i))
	}

	// ── PREMISES, read out of the table rather than derived from the loop above. Both assertions
	// below are about which rows a LIMIT keeps, so they are vacuous unless the cohort is BIGGER than
	// the limits and the costs are all DIFFERENT — a fixture that silently seeded fewer rows, or two
	// rows at the same cost, would make them hold for a reason unrelated to any ordering.
	var qualifying, distinctCosts int
	if err := d.Pool.QueryRow(ctx, `
        SELECT COUNT(*), COUNT(DISTINCT ai_cost_usd) FROM issues
         WHERE workspace_id = $1 AND ai_cost_usd > 0`, ws.ID).Scan(&qualifying, &distinctCosts); err != nil {
		t.Fatalf("read the cohort premise: %v", err)
	}
	if qualifying != aicostRankedIssues || distinctCosts != aicostRankedIssues {
		t.Fatalf("[O-PREMISE] the cohort is %d row(s) at %d distinct costs, want %d and %d — with a "+
			"cohort at or under the LIMIT, or with ties, nothing below can fail",
			qualifying, distinctCosts, aicostRankedIssues, aicostRankedIssues)
	}

	// ── WHAT THIS FILE CANNOT SEE, MEASURED AND LOGGED RATHER THAN LEFT FOR SOMEBODY TO DISCOVER.
	// migrations/0009_analytics.sql creates idx_issues_ai_cost ON issues(workspace_id, ai_cost_usd
	// DESC) WHERE ai_cost_usd > 0 — a partial index whose second key is ALREADY DESCENDING — and
	// EXPLAIN on the shipped leaderboard statement reports `Index Scan using idx_issues_ai_cost`
	// WITH the ORDER BY and WITHOUT it alike. So on this data shape a leaderboard that DELETED its
	// ORDER BY returns the correct rows in the correct order, and no assertion here can tell.
	// That is the honest scope of this guard: it catches ranking the wrong WAY and shipping the
	// wrong ROWS, not the removal of a clause the planner happens to satisfy for free.
	//
	// It is LOGGED and not ASSERTED on purpose. The order the planner hands back is a fact about
	// the plan, not a promise of the product; pinning it would red the day the planner picks a seq
	// scan — for a cohort size, not a defect — and that is exactly the kind of guard this repo has
	// been unpicking. The ORDER BY is what makes the order a promise rather than a plan artefact,
	// and index_claims_realpg_test.go is where the index's own claim is pinned.
	unordered := []float64{}
	if rows, err := d.Pool.Query(ctx, `
        SELECT ai_cost_usd FROM issues
         WHERE workspace_id = $1 AND ai_cost_usd > 0
           AND updated_at > NOW() - (INTERVAL '1 day' * 30) LIMIT 10`, ws.ID); err == nil {
		for rows.Next() {
			var c float64
			if err := rows.Scan(&c); err == nil {
				unordered = append(unordered, c)
			}
		}
		rows.Close()
	}
	t.Logf("MEASURED, NOT ASSERTED: the same predicate with NO ORDER BY returns %v — "+
		"idx_issues_ai_cost is keyed (workspace_id, ai_cost_usd DESC), so deleting the leaderboard's "+
		"ORDER BY is invisible on this data shape and this file does not claim otherwise", unordered)

	e := analytics.New(d.Pool)
	out, err := e.GetAICostTrends(ctx, ws.ID, 30)
	if err != nil {
		t.Fatalf("GetAICostTrends: %v", err)
	}

	// ── THE LEADERBOARD.
	if len(out.TopCostIssues) != 10 {
		t.Fatalf("[L-LIMIT] the leaderboard returned %d row(s) over a cohort of %d, want 10 — the "+
			"LIMIT is what makes the ordering a membership decision: %+v",
			len(out.TopCostIssues), aicostRankedIssues, out.TopCostIssues)
	}
	for i := 1; i < len(out.TopCostIssues); i++ {
		if out.TopCostIssues[i].CostUSD > out.TopCostIssues[i-1].CostUSD {
			t.Errorf("[L-ORDER] leaderboard row %d ($%v) costs MORE than row %d ($%v) — the list is "+
				"served in cost order and mcp.Server.toolGetAICosts publishes its first five as "+
				"\"top-5 most expensive issues\": %+v",
				i, out.TopCostIssues[i].CostUSD, i-1, out.TopCostIssues[i-1].CostUSD, out.TopCostIssues)
		}
	}

	// MEMBERSHIP, which is the half an ordering assertion alone cannot reach: the ten most
	// expensive issues must BE the leaderboard, and the eleven cheapest must be absent from it.
	onBoard := map[string]float64{}
	for _, ic := range out.TopCostIssues {
		onBoard[ic.Identifier] = ic.CostUSD
	}
	for n := aicostRankedIssues; n > aicostRankedIssues-10; n-- {
		id := fmt.Sprintf("RANK-%d", n)
		if _, ok := onBoard[id]; !ok {
			t.Errorf("[L-MEMBERSHIP] %s carries $%d and is NOT on the leaderboard: %+v",
				id, n, out.TopCostIssues)
		}
	}
	for n := 1; n <= aicostRankedIssues-10; n++ {
		id := fmt.Sprintf("RANK-%d", n)
		if _, ok := onBoard[id]; ok {
			t.Errorf("[L-MEMBERSHIP] %s carries $%d — one of the %d CHEAPEST issues in the workspace "+
				"— and it is on a leaderboard of the ten most expensive: %+v",
				id, n, aicostRankedIssues-10, out.TopCostIssues)
		}
	}

	// THE PUBLISHED CLAIM, ASSERTED AS THE AGENT SEES IT. `top[:5]` is a prefix, so this is the
	// exact statement the tool description makes.
	for i := 0; i < 5; i++ {
		want := fmt.Sprintf("RANK-%d", aicostRankedIssues-i)
		if out.TopCostIssues[i].Identifier != want {
			t.Errorf("[L-PREFIX] position %d of the leaderboard is %s, want %s — mcp.toolGetAICosts "+
				"serves top[:5] verbatim as \"top-5 most expensive issues\"",
				i, out.TopCostIssues[i].Identifier, want)
		}
	}

	// ── COST BY LABEL: the same shape, its own LIMIT, its own ORDER BY, and one label per issue so
	// each label's summed cost is that issue's cost.
	if len(out.CostByLabel) != 20 {
		t.Fatalf("[B-LIMIT] cost_by_label returned %d bucket(s) over %d distinct labels, want 20: %+v",
			len(out.CostByLabel), aicostRankedIssues, out.CostByLabel)
	}
	for i := 1; i < len(out.CostByLabel); i++ {
		if out.CostByLabel[i].CostUSD > out.CostByLabel[i-1].CostUSD {
			t.Errorf("[B-ORDER] cost_by_label row %d ($%v) costs MORE than row %d ($%v): %+v",
				i, out.CostByLabel[i].CostUSD, i-1, out.CostByLabel[i-1].CostUSD, out.CostByLabel)
		}
	}
	for _, lc := range out.CostByLabel {
		if lc.Label == "rank-label-1" {
			t.Errorf("[B-MEMBERSHIP] the CHEAPEST label ($1.00) is in a 20-bucket ranking of 21 "+
				"labels, so the one dropped was not the cheapest: %+v", out.CostByLabel)
		}
	}

	// ── MUST-STAY-TRUE COMPANION, and it is what keeps the two rankings above from being read as a
	// claim about the whole report: the TOTAL is over the entire cohort and is NOT truncated by
	// either LIMIT. 1+2+…+21 = 231.
	if out.TotalCostUSD != 231 {
		t.Errorf("[R-TOTAL] total_cost_usd = %v, want 231 — the total sums the whole cohort while "+
			"the two rankings above ship 10 and 20 rows of it", out.TotalCostUSD)
	}
}
