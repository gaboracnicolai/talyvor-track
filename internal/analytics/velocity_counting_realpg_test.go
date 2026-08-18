package analytics_test

// EVERY COUNTING RULE OF THE VELOCITY REPORT WAS UNASSERTED, AND THE TEAM SCOPE COULD BE DELETED
// WITH THE WHOLE REPOSITORY GREEN.
//
// analytics.GetVelocity's numbers are computed ENTIRELY IN SQL — three correlated subqueries, an
// ORDER BY, a LIMIT and a team/workspace scope — and only `completion_rate` is arithmetic Go does.
// The three tests that named it (engine_test.go's TestGetVelocity_ReturnsCompletionRates,
// ...IncludesAICostPerCycle, TestExportVelocityCSV_ProducesValidCSV) are pgxmock tests: they FEED
// the row `("c-1","Sprint 1",now,now,10,8,4.50)` and assert the struct carries 8/10. They assert
// row→struct scanning and the division, which are worth having and are not the SQL's rules. This is
// the same shape #152 found one report over in GetWorkload, in the same file, from the same cause.
//
// MEASURED, NOT INFERRED, at f11c121 — one term of the shipped SQL mutated at a time, each run over
// the whole analytics package, each restored in a `finally` and sha256-verified
// (scripts/w34-velocity-counting-controls-8f5c.py):
//
//	V1  completed FILTER drops 'cancelled'            completion rate falls    NOT CAUGHT
//	V2  completed FILTER takes 'in_review'            completion rate rises    NOT CAUGHT
//	V3  ORDER BY c.number DESC -> ASC                 the OLDEST N cycles      NOT CAUGHT
//	V4  LIMIT $3 -> LIMIT GREATEST($3,50)             `cycles` ignored         NOT CAUGHT
//	V5  total subquery drops `cycle_id = c.id`        every issue counted      NOT CAUGHT
//	V6  ai_cost subquery drops `cycle_id = c.id`      every issue summed       NOT CAUGHT
//	V7  the team scope neutralised                    every team's cycles      NOT CAUGHT
//
// Seven for seven green. ⚠ V7 IS THE ONE TO READ, AND IT TOOK THREE SPELLINGS TO MEASURE HONESTLY.
// Its first two spellings scored CAUGHT by all three pgxmock tests — and neither catch was an
// assertion. Those tests match the statement with `ExpectQuery("FROM cycles c\\s+WHERE c.team_id")`,
// a QUERY-TEXT fingerprint, so they red whenever the SQL string stops matching the regex, whatever
// the new statement computes. The third spelling leaves `WHERE c.team_id` byte-identical and
// neutralises the filter with `ANY(ARRAY[$1, c.team_id])`; it scores NOT CAUGHT. A mock cannot see a
// predicate it supplies the answer to, and a text fingerprint cannot tell a fix from a defect.
//
// The scope of that measurement is the whole population and not a sample: no file outside
// internal/analytics references GetVelocity, ExportVelocityCSV or the velocity route (the one hit in
// internal/importer is a comment), so the analytics package IS every test that could catch these.
//
// ⚠ WHAT THIS FILE PINS AND DOES NOT ENDORSE — A CANCELLED ISSUE IS "COMPLETED" HERE AND
// "REMAINING" IN THE BURNDOWN OF THE SAME CYCLE. `completed` is `status IN ('done','cancelled')`
// while analytics.GetBurndown counts a cycle down by `completed_at IS NOT NULL`, and Track stamps
// completed_at ONLY on a transition to done (issue/store.go:964, and the importer inherits it —
// jira.go:261, linear.go:89). So a cycle that ends with cancelled work reports a completion rate
// ABOVE the fraction its own burndown ever burns down, on the same screen. Pinned below as two
// numbers rather than a sentence: rate = 1.0 while the burndown's last point still shows remaining
// work. WHICH ONE IS RIGHT IS A PRODUCT DECISION — cancelled work is not delivered, and it is also
// not outstanding — so it is measured here so it cannot change silently in either direction.
//
// ⚠ AND AN IMPORT IS WHAT REACHES IT: both transports map a provider's abandoned states onto
// Track's `cancelled` and deliberately leave completed_at NULL, so an imported backlog is exactly
// the fixture below.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// seedVelocityCycle inserts a cycle whose window is CLOSED (it started 30 days ago and ended
// yesterday), which is what a velocity report is about. The name is the leak canary.
func seedVelocityCycle(t *testing.T, d *testutil.DB, wsID, teamID, name string, number int) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO cycles (team_id, workspace_id, name, number, start_date, end_date)
         VALUES ($1,$2,$3,$4, NOW() - INTERVAL '30 days', NOW() - INTERVAL '1 day') RETURNING id`,
		teamID, wsID, name, number).Scan(&id); err != nil {
		t.Fatalf("seed cycle %s: %v", name, err)
	}
	return id
}

// seedVelocityIssue writes one issue into a cycle. completedSQL is SQL rather than a Go time so the
// fixture and the query share one clock, and so a case can say "done but never stamped" — the state
// an import writes — without the test process inventing an instant.
func seedVelocityIssue(t *testing.T, d *testutil.DB, wsID, teamID string, cycleID *string, n int, status, completedSQL string, cost float64) {
	t.Helper()
	_, err := d.Pool.Exec(context.Background(), `
        INSERT INTO issues (workspace_id, team_id, cycle_id, number, identifier, title, status,
                            priority, creator_id, completed_at, ai_cost_usd)
        VALUES ($1, $2, $3, $4::int, 'VL-' || $4::int, 'velocity ' || $4::int, $5, 0, 'vlprobe', `+completedSQL+`, $6)`,
		wsID, teamID, cycleID, n, status, cost)
	if err != nil {
		t.Fatalf("seed issue %d (%s): %v", n, status, err)
	}
}

func TestGetVelocity_TheSQLsOwnCountingRules_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	teamA := d.Team(t, ws.ID)
	teamB := d.Team(t, ws.ID)

	// Four cycles in team A, numbered so the report's "last N" is a claim with an answer.
	_ = seedVelocityCycle(t, d, ws.ID, teamA.ID, "A-Sprint-1", 1)
	c2 := seedVelocityCycle(t, d, ws.ID, teamA.ID, "A-Sprint-2", 2)
	c3 := seedVelocityCycle(t, d, ws.ID, teamA.ID, "A-Sprint-3", 3)
	_ = seedVelocityCycle(t, d, ws.ID, teamA.ID, "A-Sprint-4", 4)
	// One cycle in team B, same workspace — the cross-TEAM canary. The existing scope test covers
	// cross-WORKSPACE; nothing covered this, which is why V7 was green.
	cB := seedVelocityCycle(t, d, ws.ID, teamB.ID, "B-Sprint-9", 9)

	// ── CYCLE 3 is the counted one. Every expected number in it is DISTINCT from every other, so
	// no mutation of one term can land on another's expected value by luck:
	//	total = 8 · completed = 5 (3 done + 2 cancelled) · ai_cost = 7.00 · rate = 0.625
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c3, 1, "done", "NOW() - INTERVAL '9 days'", 1.00)
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c3, 2, "done", "NOW() - INTERVAL '8 days'", 1.00)
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c3, 3, "done", "NOW() - INTERVAL '7 days'", 1.00)
	// CANCELLED, completed_at NULL — the state Track's own Update produces and the importer writes.
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c3, 4, "cancelled", "NULL", 1.00)
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c3, 5, "cancelled", "NULL", 1.00)
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c3, 6, "in_review", "NULL", 1.00)
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c3, 7, "in_progress", "NULL", 1.00)
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c3, 8, "todo", "NULL", 0.00)

	// ── CYCLE 2 holds ONE issue with a large cost, and CYCLE 1 none. A subquery that stops being
	// correlated to `c.id` picks these up, which is what separates V5/V6 from a coincidence.
	seedVelocityIssue(t, d, ws.ID, teamA.ID, &c2, 9, "done", "NOW() - INTERVAL '20 days'", 50.00)
	// ── AND ONE ISSUE IN NO CYCLE AT ALL, which is where an imported issue lands: an uncorrelated
	// COUNT(*) over `issues` counts it into a cycle that never held it.
	seedVelocityIssue(t, d, ws.ID, teamA.ID, nil, 10, "todo", "NULL", 100.00)
	// ── TEAM B's cycle holds work too, so its absence from team A's report is a fact about the
	// scope rather than about an empty cycle.
	seedVelocityIssue(t, d, ws.ID, teamB.ID, &cB, 11, "done", "NOW() - INTERVAL '2 days'", 9.00)

	e := analytics.New(d.Pool)

	// ── THE LAST THREE CYCLES OF TEAM A.
	rows, err := e.GetVelocity(ctx, teamA.ID, ws.ID, 3)
	if err != nil {
		t.Fatalf("GetVelocity(team A, 3): %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("[V-LIMIT] returned %d cycles, want 3 — `cycles` is the caller's bound and team A "+
			"has four: %+v", len(rows), rows)
	}
	if rows[0].CycleName != "A-Sprint-4" || rows[1].CycleName != "A-Sprint-3" || rows[2].CycleName != "A-Sprint-2" {
		t.Errorf("[V-ORDER] got %q, %q, %q — the report is the LAST N cycles by number, newest "+
			"first; ascending order answers with the oldest three and looks exactly as healthy",
			rows[0].CycleName, rows[1].CycleName, rows[2].CycleName)
	}
	for _, r := range rows {
		if r.CycleName == "B-Sprint-9" {
			t.Fatalf("[V-TEAM] team B's cycle appears in team A's velocity report: %+v", rows)
		}
	}

	byName := map[string]analytics.CycleVelocity{}
	for _, r := range rows {
		byName[r.CycleName] = r
	}

	s3 := byName["A-Sprint-3"]
	if s3.Total != 8 {
		t.Errorf("[V-TOTAL] A-Sprint-3 total = %d, want 8 — the subquery counts THIS cycle's issues; "+
			"the workspace holds 11 and one of them is in no cycle at all", s3.Total)
	}
	if s3.Completed != 5 {
		t.Errorf("[V-COMPLETED] A-Sprint-3 completed = %d, want 5 — `status IN ('done','cancelled')`: "+
			"3 done + 2 cancelled, and in_review/in_progress/todo are none of it", s3.Completed)
	}
	if s3.CompletionRate < 0.6249 || s3.CompletionRate > 0.6251 {
		t.Errorf("[V-RATE] A-Sprint-3 completion_rate = %v, want 0.625 (5/8)", s3.CompletionRate)
	}
	if s3.AICostUSD != 7.00 {
		t.Errorf("[V-COST] A-Sprint-3 ai_cost_usd = %v, want 7.00 — the SUM is over THIS cycle's "+
			"rows only; the workspace's other issues carry 50 and 100", s3.AICostUSD)
	}

	s2 := byName["A-Sprint-2"]
	if s2.Total != 1 || s2.Completed != 1 || s2.AICostUSD != 50.00 {
		t.Errorf("[V-NEIGHBOUR] A-Sprint-2 = total %d / completed %d / cost %v, want 1 / 1 / 50 — "+
			"a second populated cycle is what makes an uncorrelated subquery visible",
			s2.Total, s2.Completed, s2.AICostUSD)
	}
	s4 := byName["A-Sprint-4"]
	if s4.Total != 0 || s4.Completed != 0 || s4.AICostUSD != 0 || s4.CompletionRate != 0 {
		t.Errorf("[V-EMPTY] A-Sprint-4 holds no issues and reports total %d / completed %d / cost %v "+
			"/ rate %v, want zeros — COALESCE turns an empty SUM into 0 and the rate guard turns a "+
			"0/0 into 0 rather than NaN", s4.Total, s4.Completed, s4.AICostUSD, s4.CompletionRate)
	}

	// ── THE FOURTH CYCLE IS REACHABLE, so V-LIMIT above is a bound and not an empty tail.
	all, err := e.GetVelocity(ctx, teamA.ID, ws.ID, 10)
	if err != nil {
		t.Fatalf("GetVelocity(team A, 10): %v", err)
	}
	if len(all) != 4 {
		t.Errorf("[V-LIMIT-WIDE] a wider bound returned %d cycles, want all 4 of team A's", len(all))
	}

	// ── TEAM B's OWN REPORT: its cycle exists and is populated, so [V-TEAM] above is a scope
	// result rather than a claim about a cycle nothing could see.
	bRows, err := e.GetVelocity(ctx, teamB.ID, ws.ID, 5)
	if err != nil {
		t.Fatalf("GetVelocity(team B): %v", err)
	}
	if len(bRows) != 1 || bRows[0].Total != 1 || bRows[0].Completed != 1 {
		t.Fatalf("[V-TEAM-POSITIVE] team B's own velocity = %+v, want one cycle with total 1 / "+
			"completed 1", bRows)
	}

	// ── THE PINNED CONTRADICTION, AS NUMBERS. Cycle 3's two cancelled issues are `completed` to
	// this report and were never stamped completed_at, which is the only thing GetBurndown counts.
	burn, err := e.GetBurndown(ctx, c3, ws.ID)
	if err != nil {
		t.Fatalf("GetBurndown(A-Sprint-3): %v", err)
	}
	if len(burn.Points) == 0 {
		t.Fatalf("[V-BURNDOWN] the burndown of A-Sprint-3 has no points")
	}
	last := burn.Points[len(burn.Points)-1]
	burnedDown := s3.Total - last.Remaining
	if burnedDown != 3 {
		t.Errorf("[V-BURNDOWN] the burndown of A-Sprint-3 burned %d of %d issues, want 3 — it counts "+
			"`completed_at IS NOT NULL`, and only the done ones carry that", burnedDown, s3.Total)
	}
	if burnedDown >= s3.Completed {
		t.Errorf("[V-DISAGREEMENT] this file exists partly to pin that velocity's `completed` (%d) "+
			"is ABOVE what the same cycle's burndown ever burns down (%d). They now agree, which "+
			"means one of the two definitions moved — that is a product decision and this line is "+
			"where it is recorded", s3.Completed, burnedDown)
	}
	t.Logf("PINNED (measured, not endorsed): A-Sprint-3 reports completion_rate %.3f (%d/%d, "+
		"cancelled counted as completed) while its own burndown still shows %d of %d remaining at "+
		"the last point (cancelled work is never stamped completed_at). Two numbers for one cycle, "+
		"on one screen.", s3.CompletionRate, s3.Completed, s3.Total, last.Remaining, s3.Total)
}
