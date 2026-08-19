package analytics_test

// FOUR OF THE AI-COST REPORT'S FIVE SUB-QUERIES CAN BE MADE TO ANSWER A DIFFERENT QUESTION WITH
// EVERY TEST IN THIS REPOSITORY GREEN — AND THE FIFTH IS "COVERED" BY AN INDEX CENSUS THAT IS NOT
// LOOKING AT THE NUMBER.
//
// Takes tab-8f3d's handed-on lead (c): the (B) DIRECTION of the `ExpectQuery` census. The (A)
// direction (scripts/w34-expectquery-fingerprint-census-8f3d.py) asked whether each of
// engine_test.go's 13 `pool.ExpectQuery(` sites reds on a rewrite that changes the statement TEXT
// and nothing the product does — 10 of 13 did. (B) asks the harder half: change what the statement
// ANSWERS while holding the matched substring BYTE-IDENTICAL, and see whether anything reds.
//
// MEASURED at 3dbbec1 over the whole import closure of engine.go — internal/analytics,
// internal/importer (its date/resolution job tests call GetTimeToResolution on real Postgres),
// internal/mcp and cmd/track, which `go list` says are the only four packages that compile against
// this file — one term at a time, each restored in a `finally` with engine.go sha256-verified
// (scripts/w34-expectquery-behaviour-census-9a7c.py):
//
//	engine_test.go:39   velocity `completed` drops 'cancelled'                CAUGHT
//	engine_test.go:68   velocity's ai-cost subquery DE-CORRELATED             CAUGHT
//	engine_test.go:94   distribution money SUM -> MAX                         CAUGHT
//	engine_test.go:124  distribution window created_at -> updated_at          CAUGHT
//	engine_test.go:151  resolution avg_hours served in MINUTES                **BLIND**
//	engine_test.go:159  resolution per-priority MEDIAN -> p95                 **BLIND**
//	engine_test.go:187  avg_cost_per_issue divides by EVERY issue             **BLIND**  <- here
//	engine_test.go:191  daily series buckets by created_at                    **BLIND**  <- here
//	engine_test.go:202  zero-cost issues enter the leaderboard                CAUGHT, see below
//	engine_test.go:207  cost_by_team SUM -> MAX                               **BLIND**  <- here
//	engine_test.go:212  cost_by_label SUM -> MAX                              **BLIND**  <- here
//	engine_test.go:245  workload member cost SUM -> MAX                       CAUGHT
//	engine_test.go:271  velocity export ORDER BY DESC -> ASC                  CAUGHT
//
// This file closes the four that belong to `GetAICostTrends`. The two resolution ones are a
// different report and are handed on rather than ridden in on this diff.
//
// ⚠⚠ READ engine_test.go:202 BEFORE BELIEVING IT IS COVERED, BECAUSE IT IS THE CLASS THIS QUEUE
// KEEPS FINDING. Relaxing the leaderboard's `ai_cost_usd > 0` to `>= 0` — which lets zero-cost
// issues pad out "the ten most expensive issues" — scores CAUGHT. The single test that reds is
// TestAnalyticsIndexes_TheEngineOnlyScansOneOfTheThree0009Declares_RealPG, and it reds because the
// predicate no longer matches the PARTIAL INDEX `idx_issues_ai_cost ... WHERE ai_cost_usd > 0`, so
// the planner stops using it. NOTHING IN THIS REPOSITORY ASSERTS THAT A $0.00 ISSUE STAYS OUT OF
// THE LEADERBOARD. An index census standing in for a value assertion is coverage in name only, and
// the day that index is dropped or the planner changes its mind the term is unguarded. Recorded
// rather than fixed here: one merge per finding, and this one is the four sub-queries below.
//
// ⚠ WHY THE EXISTING REAL-POSTGRES TESTS COULD NOT SEE ANY OF THE FOUR, MEASURED FROM THEIR
// FIXTURES RATHER THAN ASSUMED — every one is a shape that makes SUM and MAX the same number, or
// the two denominators the same number:
//
//   - aicost_ordering_realpg_test.go seeds 21 issues each with ITS OWN distinct label
//     (`ARRAY['rank-label-' || $3]`) and every cost strictly positive. One issue per label means
//     SUM(ai_cost_usd) = MAX(ai_cost_usd) per label, so cost_by_label's aggregate is unfalsifiable
//     there BY CONSTRUCTION; and with no zero-cost row, COUNT(*) and COUNT(*) FILTER (WHERE
//     ai_cost_usd > 0) are the same integer, so avg_cost_per_issue's denominator is too. Its subject
//     is which rows the two LIMITs keep, and it is good at that.
//   - aicost_window_test.go's cohorts are one and two issues. Every aggregate over a one-row group
//     is that row.
//   - scope_read_test.go seeds one cost-carrying issue per workspace — same.
//   - index_claims_realpg_test.go drives every engine method and reads pg_stat_user_indexes. It
//     asserts which INDEX was scanned and never reads a number the report returned.
//
// So this fixture is built the other way round on purpose: a label held by THREE issues, a team
// holding FIVE, zero-cost rows sitting inside the cohort next to paying ones, and one issue whose
// created_at day is not its updated_at day. Each of those four properties is re-measured IN-PROCESS
// by an `[A-PREMISE-*]` probe that reads the DATABASE with its own SQL, not the engine's — without
// them this file would pass identically on a fixture that had quietly become uniform again, which
// is exactly how the four terms above stayed blind through five earlier merges.
//
// ⚠ WHAT THE NUMBERS MEAN, since three of the four are money on a surface an agent spends against.
// `mcp.Server.toolGetAICosts` publishes this report to an agent under "total spend, top-5 most
// expensive issues, and projected monthly spend"; `avg_cost_per_issue` with the wrong denominator
// UNDERSTATES what a paying issue costs by exactly the share of the cohort that cost nothing, and a
// cost_by_team that reports a team's most expensive ISSUE as its TOTAL understates a busy team and
// is exactly right for a team with one issue — so the error is invisible on the small workspace
// where somebody would first look. Neither is a rounding difference; both are a different question.
//
// ⚠ THIS FILE PASSED ON ITS FIRST RUN, SO IT IS CONTROLLED BOTH WAYS — 9 controls, each naming its
// predicted verdict AND its predicted catching tag BEFORE it ran, each restored in a `finally` with
// both mutated files sha256-verified back to pristine (scripts/w34-aicost-arithmetic-controls-9a7c.py):
//
//	A1  the FILTER dropped from the totals row      CAUGHT by [A-AVG-COHORT] ALONE
//	A2  the daily series keyed on created_at        CAUGHT by [A-DAY-KEY] ALONE
//	A3  cost_by_team SUM -> MAX                     CAUGHT by [A-TEAM-SUM] ALONE
//	A4  cost_by_label SUM -> MAX                    CAUGHT by [A-LABEL-SUM] ALONE
//	A5  cost_by_team ranked ASC                     NOT CAUGHT — must stay green, ordering is
//	A6  the leaderboard ranked ASC                  NOT CAUGHT — aicost_ordering's subject
//	A7  the FIXTURE's day-split removed             CAUGHT by [A-PREMISE-DAYSPLIT] ALONE
//	A8  the FIXTURE's zero-cost rows given a cost   CAUGHT by [A-PREMISE-COHORT] ALONE
//	A9  [A-AVG-COHORT] deleted WITH A1 on top       NOT CAUGHT anywhere in the closure
//
// A5 and A6 are what stop the four tags reading as "the whole report". A9 is the measured
// blindness: re-run here over all four packages rather than cited from the census.
//
// ⚠⚠ AND THE CONTROL HARNESS WAS WRONG TWICE BEFORE THE PRODUCT WAS, BOTH IN THE FLATTERING
// DIRECTION, which is the part worth keeping. (1) It scraped `[A-*]` tags with a bare `findall`
// over the whole output — but several assertions NAME another tag in their prose ("[A-DAY-KEY]
// below cannot fail"), so A7 and A8 each scored TWO catchers and read as tags that fire for more
// than one reason, which is the one property that would disqualify them. A sentence about a tag is
// not a tag; it now reads only the tag a failure LINE OPENS WITH. That is the same off-by-one the
// (A) census header records against `grep -c 'ExpectQuery('`. (2) A9's first spelling decided which
// of the closure's failures were the pre-existing empty-corpus ones BY MATCHING THEIR NAMES for
// "Corpus"/"Census" — and TestJiraCSVLayoutSupport_EveryPinnedLayoutHasADistinctExportBehindIt
// contains neither, so it counted as a real red and A9 reported CAUGHT. Read as written, that says
// "something already covers this" and the whole finding evaporates. It now subtracts a MEASURED C0
// set. Neither error was visible by reading the harness; both surfaced only because the controls
// carried predictions that could be missed.
//
// ⚠ NOT ASSERTED HERE, SAID RATHER THAN IMPLIED: the report's cohort. Every figure in
// GetAICostTrends is still "the LIFETIME ai_cost_usd of issues TOUCHED in the window" rather than
// "spend in the window" — engine.go says so at the leaderboard and names ai_spend_events as the
// table that could answer the other question. This file pins the arithmetic over the cohort the
// product defines; it does not endorse the cohort.

import (
	"context"
	"fmt"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// seedArithCostIssue writes one issue with an explicit cost, label set and created/updated SQL. Every
// row carries the same loud ai_tokens so a money column swapped for the token column is visible in
// the assertion's own message rather than as a plausible-looking number.
func seedArithCostIssue(t *testing.T, d *testutil.DB, wsID, teamID string, n int,
	cost float64, labels []string, createdSQL, updatedSQL string) {
	t.Helper()
	_, err := d.Pool.Exec(context.Background(), `
        INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
                            creator_id, ai_cost_usd, ai_tokens, labels, created_at, updated_at)
        VALUES ($1, $2, $3::int, 'AC-' || $3::int, 'aicost ' || $3::int, 'done', 1, 'acprobe',
                $4, 424242, $5, `+createdSQL+`, `+updatedSQL+`)`,
		wsID, teamID, n, cost, labels)
	if err != nil {
		t.Fatalf("seed cost issue %d (cost %v): %v", n, cost, err)
	}
}

func arithScalarFloat(t *testing.T, d *testutil.DB, sql string, args ...any) float64 {
	t.Helper()
	var v float64
	if err := d.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("probe %q: %v", sql, err)
	}
	return v
}

func arithScalarInt(t *testing.T, d *testutil.DB, sql string, args ...any) int {
	t.Helper()
	var v int
	if err := d.Pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("probe %q: %v", sql, err)
	}
	return v
}

func arithNearly(got, want float64) bool { return got-want < 0.0001 && want-got < 0.0001 }

func TestGetAICostTrends_TheSQLsOwnArithmetic_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	eng := d.Team(t, ws.ID)
	ops := d.Team(t, ws.ID)

	// Every row is touched inside the window, so the COHORT is the whole fixture and no assertion
	// below is really a window assertion in disguise (aicost_window_test.go owns the window).
	const touched = "NOW() - INTERVAL '2 hours'"
	const bornLongBefore = "NOW() - INTERVAL '10 days'"

	//   #  team  cost   labels      created           updated
	//   1  ENG   5.00   shared      touched           touched
	//   2  ENG   3.00   shared      touched           touched
	//   3  OPS   1.00   solo        touched           touched
	//   4  OPS   0.25   solo        touched           touched
	//   5  ENG   0.00   free        touched           touched
	//   6  ENG   0.00   free        touched           touched
	//   7  ENG   2.00   shared      TEN DAYS AGO      touched
	seedArithCostIssue(t, d, ws.ID, eng.ID, 1, 5.00, []string{"shared"}, touched, touched)
	seedArithCostIssue(t, d, ws.ID, eng.ID, 2, 3.00, []string{"shared"}, touched, touched)
	seedArithCostIssue(t, d, ws.ID, ops.ID, 3, 1.00, []string{"solo"}, touched, touched)
	seedArithCostIssue(t, d, ws.ID, ops.ID, 4, 0.25, []string{"solo"}, touched, touched)
	seedArithCostIssue(t, d, ws.ID, eng.ID, 5, 0.00, []string{"free"}, touched, touched)
	seedArithCostIssue(t, d, ws.ID, eng.ID, 6, 0.00, []string{"free"}, touched, touched)
	seedArithCostIssue(t, d, ws.ID, eng.ID, 7, 2.00, []string{"shared"}, bornLongBefore, touched)

	const (
		wantTotal   = 11.25 // 5 + 3 + 1 + 0.25 + 0 + 0 + 2
		wantPaying  = 5     // rows with ai_cost_usd > 0
		wantAllRows = 7
		wantAvg     = wantTotal / wantPaying // 2.25
		wantENG     = 10.00                  // 5 + 3 + 0 + 0 + 2
		wantOPS     = 1.25                   // 1 + 0.25
		wantShared  = 10.00                  // 5 + 3 + 2
		wantSolo    = 1.25                   // 1 + 0.25
	)

	// ── PREMISE PROBES. Each reads the DATABASE with its own SQL — not the engine's — and asserts
	// the fixture property WITHOUT which the assertion beneath it cannot fail. They are the reason
	// this file cannot quietly become the uniform fixtures described in the header.
	if paying, all := arithScalarInt(t, d, `SELECT COUNT(*) FROM issues WHERE workspace_id=$1 AND ai_cost_usd > 0`, ws.ID),
		arithScalarInt(t, d, `SELECT COUNT(*) FROM issues WHERE workspace_id=$1`, ws.ID); paying == all {
		t.Fatalf("[A-PREMISE-COHORT] %d of %d issues carry a cost — with no ZERO-cost row in the "+
			"cohort, COUNT(*) and COUNT(*) FILTER (WHERE ai_cost_usd > 0) are the same integer and "+
			"[A-AVG-COHORT] below cannot fail", paying, all)
	} else if paying != wantPaying || all != wantAllRows {
		t.Fatalf("[A-PREMISE-COHORT] cohort is %d paying / %d total, want %d / %d — the expected "+
			"numbers below are read off a different fixture", paying, all, wantPaying, wantAllRows)
	}
	if split := arithScalarInt(t, d, `SELECT COUNT(*) FROM issues WHERE workspace_id=$1
        AND date_trunc('day', created_at) <> date_trunc('day', updated_at)`, ws.ID); split != 1 {
		t.Fatalf("[A-PREMISE-DAYSPLIT] %d issues have a created_at DAY different from their "+
			"updated_at DAY, want 1 — with none, keying the daily series off either column puts "+
			"every row in the same bucket and [A-DAY-KEY] below cannot fail", split)
	}
	if s, m := arithScalarFloat(t, d, `SELECT SUM(ai_cost_usd) FROM issues WHERE team_id=$1`, eng.ID),
		arithScalarFloat(t, d, `SELECT MAX(ai_cost_usd) FROM issues WHERE team_id=$1`, eng.ID); arithNearly(s, m) {
		t.Fatalf("[A-PREMISE-TEAM] team ENG's SUM (%v) equals its MAX (%v) — an aggregate is "+
			"unfalsifiable over a group whose rows cannot distinguish it, and [A-TEAM-SUM] below "+
			"cannot fail", s, m)
	}
	if s, m := arithScalarFloat(t, d, `SELECT SUM(ai_cost_usd) FROM issues, UNNEST(labels) l
        WHERE workspace_id=$1 AND l='shared'`, ws.ID),
		arithScalarFloat(t, d, `SELECT MAX(ai_cost_usd) FROM issues, UNNEST(labels) l
        WHERE workspace_id=$1 AND l='shared'`, ws.ID); arithNearly(s, m) {
		t.Fatalf("[A-PREMISE-LABEL] label 'shared' has SUM (%v) = MAX (%v) — this is the shape "+
			"aicost_ordering_realpg_test.go's one-issue-per-label fixture has, and it is why "+
			"[A-LABEL-SUM] below has never been able to fail", s, m)
	}

	e := analytics.New(d.Pool)
	out, err := e.GetAICostTrends(ctx, ws.ID, 30)
	if err != nil {
		t.Fatalf("GetAICostTrends: %v", err)
	}

	// ── [A-AVG-COHORT] — engine_test.go:187. The average's DENOMINATOR is the issues that cost
	// something, not the issues in the window. Dropping the FILTER leaves the regex
	// `SELECT COALESCE\(SUM\(ai_cost_usd\), 0\), COUNT\(\*\)` matching byte-for-byte.
	if !arithNearly(out.TotalCostUSD, wantTotal) {
		t.Fatalf("[A-TOTAL] total_cost_usd = %v, want %v — the premise of every figure below; every "+
			"row carries 424242 ai_tokens, so a column swap shows up here first",
			out.TotalCostUSD, wantTotal)
	}
	if !arithNearly(out.AvgCostPerIssue, wantAvg) {
		t.Errorf("[A-AVG-COHORT] avg_cost_per_issue = %v, want %v = $%v / %d PAYING issues. "+
			"%v is $%v / %d, which is every issue in the window — an average over a denominator "+
			"that includes issues nothing was spent on understates what a paying issue costs by "+
			"exactly the share of the cohort that was free",
			out.AvgCostPerIssue, wantAvg, wantTotal, wantPaying,
			wantTotal/wantAllRows, wantTotal, wantAllRows)
	}

	// ── [A-DAY-KEY] — engine_test.go:191. The series is keyed on the SAME column the window
	// filters. Issue 7 was created ten days before it was touched; keyed on created_at it leaves
	// today's bucket for one of its own. The regex `date_trunc\('day'` matches either spelling.
	if len(out.DailyCosts) != 1 {
		t.Errorf("[A-DAY-KEY] the daily series has %d buckets, want 1 — every issue in this fixture "+
			"was TOUCHED within the same two hours, so a series keyed on updated_at (the column the "+
			"window itself filters) has exactly one day in it. Two buckets means the series is keyed "+
			"on created_at and the chart's x-axis is not the axis the cohort was chosen on: %+v",
			len(out.DailyCosts), out.DailyCosts)
	} else {
		if !arithNearly(out.DailyCosts[0].CostUSD, wantTotal) {
			t.Errorf("[A-DAY-KEY] the single day bucket holds $%v, want $%v — the series must sum to "+
				"the same total printed above it", out.DailyCosts[0].CostUSD, wantTotal)
		}
		if out.DailyCosts[0].Issues != wantPaying {
			t.Errorf("[A-DAY-KEY] the single day bucket counts %d issues worked, want %d — the "+
				"per-day counter carries the same FILTER as the totals row",
				out.DailyCosts[0].Issues, wantPaying)
		}
	}

	// ── [A-TEAM-SUM] — engine_test.go:207. Per-team cost is a SUM over the team's issues. ENG holds
	// five rows whose max (5.00) is not their total (10.00). The regex
	// `JOIN teams t ON t.id = i.team_id` names the join and nothing about the aggregate.
	teams := map[string]float64{}
	for _, tc := range out.CostByTeam {
		teams[tc.TeamID] = tc.CostUSD
	}
	if len(out.CostByTeam) != 2 {
		t.Errorf("[A-TEAM-SUM] cost_by_team has %d rows, want 2 (ENG, OPS): %+v",
			len(out.CostByTeam), out.CostByTeam)
	}
	for id, want := range map[string]float64{eng.ID: wantENG, ops.ID: wantOPS} {
		if !arithNearly(teams[id], want) {
			t.Errorf("[A-TEAM-SUM] team %s cost = %v, want %v — a team's cost is the SUM of its "+
				"issues. ENG's most expensive single issue is $5.00 and its total is $10.00, so an "+
				"aggregate that reports the biggest row instead is right for a team with one issue "+
				"and understates every other team: %+v", id, teams[id], want, out.CostByTeam)
		}
	}

	// ── [A-LABEL-SUM] — engine_test.go:212. Per-label cost is a SUM over the label's issues.
	// 'shared' is on three of them. The regex `UNNEST\(labels\)` names the row expansion and
	// nothing about the aggregate.
	labels := map[string]float64{}
	for _, lc := range out.CostByLabel {
		labels[lc.Label] = lc.CostUSD
	}
	for label, want := range map[string]float64{"shared": wantShared, "solo": wantSolo, "free": 0.00} {
		if !arithNearly(labels[label], want) {
			t.Errorf("[A-LABEL-SUM] label %q cost = %v, want %v — a label's cost is the SUM over "+
				"every issue carrying it. 'shared' is on three issues (5.00, 3.00, 2.00), so SUM and "+
				"MAX differ here and cannot differ in a fixture that gives each issue its own "+
				"label: %+v", label, labels[label], want, out.CostByLabel)
		}
	}

	// ── MUST STAY GREEN, and it is what stops the four tags above reading as "the whole report".
	// This file asserts ARITHMETIC. Which rows the two LIMITed sub-queries keep, and in what order,
	// is aicost_ordering_realpg_test.go's subject; the window is aicost_window_test.go's. The only
	// thing said about the leaderboard here is the count of rows it can hold at all.
	if len(out.TopCostIssues) != wantPaying {
		t.Errorf("[A-LEADERBOARD-COHORT] the leaderboard holds %d rows, want %d — the five issues "+
			"that cost something. This is a COHORT statement, not an ordering one: %s",
			len(out.TopCostIssues), wantPaying, fmt.Sprint(out.TopCostIssues))
	}
}
