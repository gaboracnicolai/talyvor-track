package analytics_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
)

func analyticsReq(path, wsID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	return r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, "m1", authz.RoleMember))
}

// seedCycle inserts a cycle (name is the leak canary) into teamID/wsID and returns its id.
func seedCycle(t *testing.T, d *testutil.DB, wsID, teamID, name string, number int) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO cycles (team_id, workspace_id, name, number, start_date, end_date)
         VALUES ($1,$2,$3,$4, NOW(), NOW() + interval '14 days') RETURNING id`,
		teamID, wsID, name, number).Scan(&id); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	return id
}

// GET .../analytics/velocity?team_id= must be workspace-scoped: a wsA member naming a wsB team
// must not receive wsB's cycles (team_id is caller-supplied).
func TestAnalytics_Velocity_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	seedCycle(t, d, wsB.ID, teamB.ID, "B-Cycle-Velocity", 1)
	h := analytics.NewHandler(analytics.New(d.Pool))

	rr := httptest.NewRecorder()
	h.Velocity(rr, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/velocity?team_id="+teamB.ID, wsA.ID))
	if strings.Contains(rr.Body.String(), "B-Cycle-Velocity") {
		t.Fatalf("CROSS-WS LEAK: wsA caller saw wsB team's velocity cycle: %s", rr.Body.String())
	}

	// Positive: the caller's own team's cycle appears.
	teamA := d.Team(t, wsA.ID)
	seedCycle(t, d, wsA.ID, teamA.ID, "A-Cycle-Velocity", 1)
	rrA := httptest.NewRecorder()
	h.Velocity(rrA, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/velocity?team_id="+teamA.ID, wsA.ID))
	if !strings.Contains(rrA.Body.String(), "A-Cycle-Velocity") {
		t.Errorf("own-team velocity should appear; got %s", rrA.Body.String())
	}
}

// GET .../analytics/burndown?cycle_id= must be workspace-scoped: a wsA member naming a wsB
// cycle must not receive that cycle's burndown (cycle_id is caller-supplied).
func TestAnalytics_Burndown_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	cycleB := seedCycle(t, d, wsB.ID, teamB.ID, "B-Cycle-Burndown", 1)
	h := analytics.NewHandler(analytics.New(d.Pool))

	rr := httptest.NewRecorder()
	h.Burndown(rr, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/burndown?cycle_id="+cycleB, wsA.ID))
	if strings.Contains(rr.Body.String(), "B-Cycle-Burndown") {
		t.Fatalf("CROSS-WS LEAK: wsA caller saw wsB cycle's burndown: %s", rr.Body.String())
	}

	// Positive: the caller's own cycle's burndown is returned (200 with its name).
	teamA := d.Team(t, wsA.ID)
	cycleA := seedCycle(t, d, wsA.ID, teamA.ID, "A-Cycle-Burndown", 1)
	rrA := httptest.NewRecorder()
	h.Burndown(rrA, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/burndown?cycle_id="+cycleA, wsA.ID))
	if rrA.Code != http.StatusOK || !strings.Contains(rrA.Body.String(), "A-Cycle-Burndown") {
		t.Errorf("own-cycle burndown should return 200 with its name; got %d %s", rrA.Code, rrA.Body.String())
	}
}

// seedResolvedIssue inserts an issue that RESOLVED `hours` after it was created, inside the
// report's default 30-day created_at window, at the given priority.
func seedResolvedIssue(t *testing.T, d *testutil.DB, wsID, teamID string, number, priority, hours int) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
		                     creator_id, created_at, completed_at)
	     VALUES ($1,$2,$3::int, 'RES-' || $3::int, 'resolution ' || $3::int, 'done', $4,
	             'resprobe', NOW() - interval '1 hour' * $5, NOW())`,
		wsID, teamID, number, priority, hours); err != nil {
		t.Fatalf("seed resolved issue %d: %v", number, err)
	}
}

// GET .../analytics/resolution?team_id= must be workspace-scoped: a wsA member naming a wsB
// team must not receive wsB's resolution figures (team_id is caller-supplied, exactly as in
// Velocity above).
//
// ⚠ THIS REPORT CANNOT BE GUARDED THE WAY ITS TWO SIBLINGS ABOVE ARE, AND COPYING THEIR SHAPE
// WOULD HAVE SHIPPED A TEST THAT CANNOT FAIL. Velocity and Burndown each echo a caller-visible
// NAME (the cycle's), which is why `!strings.Contains(body, "B-Cycle-…")` is a real assertion
// there. ResolutionStats has NO STRING FIELD — a sample size, four COALESCEd floats and a
// priority→median map — so a canary string is absent from this body whether the scope holds or
// not. MEASURED rather than reasoned: control C3 applies the canary-shaped assertion ON TOP OF
// the scope mutation of C1 and it stays GREEN while the assertions below red. The only readable
// witness of a leak here is the COHORT SIZE.
//
// ⚠ AND THE REPORT RUNS TWO INDEPENDENTLY-SCOPED QUERIES, SO IT TAKES TWO ASSERTIONS. The
// aggregate row and the per-priority breakdown each carry their own `workspace_id = $1`;
// control C2 removes the breakdown's ALONE and sample_size does not move, so a single
// sample-size assertion would have left half this report unguarded.
//
// ⚠ WHAT WAS ACTUALLY UNGUARDED, MEASURED RATHER THAN ASSUMED — the two halves differ, and
// only ONE of them was blind. Neutralising the AGGREGATE query's scope (C1) is already caught
// today by `importer.TestResolutionReport_AnImportedBacklogOlderThanTheWindowIsNotAMeasuredZero`
// — INCIDENTALLY: that test's subject is the created_at WINDOW, and it reds only because a
// cross-workspace cohort pollutes the count it pins. Neutralising the PER-PRIORITY query's
// scope (C2) was caught by NOTHING IN THE REPOSITORY before this test. So the claim here is
// not "this report had no coverage" — it is that its coverage was one accidental side effect
// in another package's window test, and its second query had none at all.
func TestAnalytics_Resolution_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	seedResolvedIssue(t, d, wsB.ID, teamB.ID, 1, 1, 7)
	h := analytics.NewHandler(analytics.New(d.Pool))

	decode := func(t *testing.T, rr *httptest.ResponseRecorder) analytics.ResolutionStats {
		t.Helper()
		var got analytics.ResolutionStats
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode resolution body %q: %v", rr.Body.String(), err)
		}
		return got
	}

	rr := httptest.NewRecorder()
	h.Resolution(rr, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/resolution?team_id="+teamB.ID, wsA.ID))
	got := decode(t, rr)
	if got.SampleSize != 0 {
		t.Fatalf("CROSS-WS LEAK: wsA caller naming a wsB team got %d issue(s) in the resolution cohort: %s",
			got.SampleSize, rr.Body.String())
	}
	if len(got.ByPriority) != 0 {
		t.Fatalf("CROSS-WS LEAK (per-priority breakdown): wsA caller naming a wsB team got %d priority bucket(s): %s",
			len(got.ByPriority), rr.Body.String())
	}

	// Positive, and NOT decoration: every assertion above is satisfied by an empty answer, so
	// without this half a query that returned nothing at all — or a seed that inserted nothing —
	// would pass the leak check silently. Control C5 breaks the scope predicate the OTHER way
	// (own rows vanish) and only this half reds.
	teamA := d.Team(t, wsA.ID)
	seedResolvedIssue(t, d, wsA.ID, teamA.ID, 2, 2, 5)
	rrA := httptest.NewRecorder()
	h.Resolution(rrA, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/resolution?team_id="+teamA.ID, wsA.ID))
	gotA := decode(t, rrA)
	if rrA.Code != http.StatusOK || gotA.SampleSize != 1 {
		t.Errorf("own-team resolution cohort should be exactly the 1 seeded issue; got %d sample_size %d: %s",
			rrA.Code, gotA.SampleSize, rrA.Body.String())
	}
	if _, ok := gotA.ByPriority["2"]; !ok {
		t.Errorf("own-team per-priority breakdown should carry the seeded priority 2; got %v: %s",
			gotA.ByPriority, rrA.Body.String())
	}
}

// seedCostIssue inserts an issue carrying AI spend, a distinctive identifier/title and a
// label, inside the AI-cost report's default 30-day updated_at window (NOT created_at — that
// report windows on last touch).
func seedCostIssue(t *testing.T, d *testutil.DB, wsID, teamID, tag string, number int, cost float64) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
		                     creator_id, ai_cost_usd, ai_tokens, labels, updated_at)
	     VALUES ($1,$2,$3::int, $4 || '-' || $3::int, $4 || ' title', 'done', 1,
	             'costprobe', $5, 4242, ARRAY[$4 || '-label'], NOW())`,
		wsID, teamID, number, tag, cost); err != nil {
		t.Fatalf("seed cost issue %s-%d: %v", tag, number, err)
	}
}

// GET .../analytics/ai-costs must be workspace-scoped.
//
// ⚠ THIS ONE CANNOT BE WRITTEN IN THE SHAPE OF THE THREE TESTS ABOVE, AND THAT IS A FACT ABOUT
// THE ENDPOINT RATHER THAN A CHOICE. Velocity, Burndown and Resolution each take a
// CALLER-SUPPLIED id (team_id, cycle_id, team_id) and so can be attacked as a confused deputy:
// a wsA member NAMES a wsB object. `AICosts` takes no selector at all — the handler passes the
// authorized `wsID` and `days` and nothing else — so there is no id to point at another
// workspace and the confused-deputy test is IMPOSSIBLE here. The reachable question is the
// weaker but still real one: does wsB's spend appear in wsA's report at all.
//
// ⚠ AND IT TAKES FIVE ASSERTIONS BECAUSE THE REPORT RUNS FIVE INDEPENDENTLY-SCOPED QUERIES —
// totals, the daily series, the top-cost leaderboard, cost-by-team and cost-by-label each
// carry their own `workspace_id = $1`. Controls C1–C5 neutralise them ONE AT A TIME; each is
// caught by its own assertion and by no other, which is the measurement that says five
// assertions are five guards rather than one guard written five times.
//
// ⚠ UNLIKE THE RESOLUTION REPORT DIRECTLY ABOVE, THIS BODY DOES CARRY STRING WITNESSES
// (identifier, title, team name, label), so the canary that is vacuous there is real here —
// which is exactly why the shape has to be chosen per report instead of copied.
func TestAnalytics_AICosts_WorkspaceScoped(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	seedCostIssue(t, d, wsB.ID, teamB.ID, "BCOST", 1, 101)
	h := analytics.NewHandler(analytics.New(d.Pool))

	decode := func(t *testing.T, rr *httptest.ResponseRecorder) analytics.AICostTrends {
		t.Helper()
		var got analytics.AICostTrends
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode ai-costs body %q: %v", rr.Body.String(), err)
		}
		return got
	}

	rr := httptest.NewRecorder()
	h.AICosts(rr, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/ai-costs", wsA.ID))
	got, body := decode(t, rr), rr.Body.String()

	if got.TotalCostUSD != 0 || got.AvgCostPerIssue != 0 || got.ProjectedMonthly != 0 {
		t.Errorf("CROSS-WS LEAK (totals): wsA saw wsB spend — total %v avg %v projected %v: %s",
			got.TotalCostUSD, got.AvgCostPerIssue, got.ProjectedMonthly, body)
	}
	if len(got.DailyCosts) != 0 {
		t.Errorf("CROSS-WS LEAK (daily series): wsA got %d day(s) of wsB spend: %s", len(got.DailyCosts), body)
	}
	if len(got.TopCostIssues) != 0 || strings.Contains(body, "BCOST-1") {
		t.Errorf("CROSS-WS LEAK (top-cost leaderboard): wsA got %d wsB issue(s): %s", len(got.TopCostIssues), body)
	}
	if len(got.CostByTeam) != 0 || strings.Contains(body, teamB.Name) {
		t.Errorf("CROSS-WS LEAK (cost by team): wsA got %d wsB team(s), name %q: %s", len(got.CostByTeam), teamB.Name, body)
	}
	if len(got.CostByLabel) != 0 || strings.Contains(body, "BCOST-label") {
		t.Errorf("CROSS-WS LEAK (cost by label): wsA got %d wsB label(s): %s", len(got.CostByLabel), body)
	}

	// Positive, and load-bearing: every assertion above is satisfied by an EMPTY report, so
	// without this half the whole test passes on a report that returns nothing — including one
	// whose seed silently inserted no row. Control C6 breaks the scope the other way and only
	// this half reds.
	teamA := d.Team(t, wsA.ID)
	seedCostIssue(t, d, wsA.ID, teamA.ID, "ACOST", 2, 55)
	rrA := httptest.NewRecorder()
	h.AICosts(rrA, analyticsReq("/v1/workspaces/"+wsA.ID+"/analytics/ai-costs", wsA.ID))
	gotA, bodyA := decode(t, rrA), rrA.Body.String()
	if rrA.Code != http.StatusOK || gotA.TotalCostUSD != 55 {
		t.Errorf("own spend should total 55; got %d %v: %s", rrA.Code, gotA.TotalCostUSD, bodyA)
	}
	if len(gotA.DailyCosts) != 1 {
		t.Errorf("own spend should be 1 day in the series; got %d: %s", len(gotA.DailyCosts), bodyA)
	}
	if len(gotA.TopCostIssues) != 1 || !strings.Contains(bodyA, "ACOST-2") {
		t.Errorf("own issue should be on the leaderboard; got %d: %s", len(gotA.TopCostIssues), bodyA)
	}
	if len(gotA.CostByTeam) != 1 || !strings.Contains(bodyA, teamA.Name) {
		t.Errorf("own team should be in cost-by-team; got %d: %s", len(gotA.CostByTeam), bodyA)
	}
	if len(gotA.CostByLabel) != 1 || !strings.Contains(bodyA, "ACOST-label") {
		t.Errorf("own label should be in cost-by-label; got %d: %s", len(gotA.CostByLabel), bodyA)
	}
}
