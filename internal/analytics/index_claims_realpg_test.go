package analytics_test

// AN INDEX IS A CLAIM ABOUT A QUERY, AND TWO OF THESE THREE CLAIMS ARE FALSE.
//
// migrations/0009_analytics.sql declares three partial indexes on `issues` and says in prose that
// they "serve the three hot query shapes the engine runs":
//
//	1. idx_issues_analytics  "completed issues in this team+window with their cost"
//	2. idx_issues_ai_cost    "top-cost issues for this workspace"
//	3. idx_issues_due        "overdue issues per assignee"
//
// Nothing had ever asked whether the engine can use them. MEASURED here, behaviourally, by driving
// EVERY exported Engine method against real Postgres and reading pg_stat_user_indexes.idx_scan
// either side — a plan I read off one hand-picked EXPLAIN would only measure the query I chose.
//
// THE RESULT: idx_issues_ai_cost is scanned. idx_issues_analytics and idx_issues_due are NOT
// scanned by any engine method. For idx_issues_due the reason is visible in the SQL — the overdue
// predicate lives inside a `COUNT(*) FILTER (...)` in GetWorkload, which Postgres applies AFTER
// the scan, while the query's WHERE is `workspace_id = $1` alone. A partial index that omits the
// rows the surrounding aggregate still needs cannot serve it, so the planner takes
// idx_issues_workspace and filters. The index's declared query shape is not a query this engine
// runs.
//
// ⚠⚠ THIS TEST FIXES NOTHING AND DELIBERATELY SO. Dropping an index is a migration and rewriting
// an aggregate is a performance change; which of those is right — or whether the third option
// (make the overdue counter its own query, which WOULD use the index) is — is a product decision
// and is written up in the queue rather than made here. What this pins is the MEASUREMENT, so the
// state cannot change silently in either direction: if someone rewrites a query and the index
// starts being used, this test fails and the author deletes the line naming it unused; if someone
// drops an index, the census below fails and says so.
//
// ⚠ WHAT MADE THE FIRST TWO RUNS OF THIS MEASUREMENT WRONG, kept here because both are the same
// class of error and both were caught by a control rather than by reading:
//
//   - A SINGLE-WORKSPACE FIXTURE GUARANTEES THE ANSWER. With all rows in one workspace,
//     `workspace_id = $1` selects 100% of the table, a seq scan is the CORRECT plan for every
//     query, and ALL THIRTEEN indexes read as unused — including ones that really are used. The
//     fixture is multi-tenant for that reason, and `assertInstrumentLive` is the guard on it.
//   - pg_stat_user_indexes IS BACKEND-LOCAL UNTIL FLUSHED. Read immediately, it reported 0 for an
//     index the EXPLAIN plan showed the planner using. A dead instrument reporting zero looks
//     exactly like a working one reporting "no reader" — so both snapshots are taken after
//     pg_stat_force_next_flush(), and the BASELINE flush matters as much as the final one: this
//     file's own warm-up query is the overdue shape, and read unflushed its scan lands in the
//     AFTER snapshot and reads as the engine using idx_issues_due. That is exactly what the first
//     run reported.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// declaredIn0009 is the pinned census of what migrations/0009_analytics.sql creates, with the
// query shape its comment claims for each. Pinned rather than derived: a guard that reads the
// migration to learn what to check cannot notice the migration losing an index.
var declaredIn0009 = map[string]string{
	"idx_issues_analytics": "completed issues in this team+window with their cost",
	"idx_issues_ai_cost":   "top-cost issues for this workspace",
	"idx_issues_due":       "overdue issues per assignee",
}

// scannedByTheEngine is the MEASURED answer, pinned. Changing the engine's SQL is what should
// change this map — and the failure message says which way it moved.
var scannedByTheEngine = map[string]bool{
	"idx_issues_analytics": false,
	"idx_issues_ai_cost":   true,
	"idx_issues_due":       false,
}

func indexScans(t *testing.T, d *testutil.DB) map[string]int64 {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT indexrelname, idx_scan FROM pg_stat_user_indexes WHERE relname = 'issues'`)
	if err != nil {
		t.Fatalf("read pg_stat_user_indexes: %v", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatalf("scan pg_stat row: %v", err)
		}
		out[name] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pg_stat rows: %v", err)
	}
	return out
}

// flushedScans forces the backend-local stats out and waits until `until` says the snapshot has
// caught up, or gives up after ~4s and returns what it has. The caller's assertions decide
// whether that is good enough; nothing here silently accepts a stale read.
func flushedScans(t *testing.T, d *testutil.DB, until func(map[string]int64) bool) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	var last map[string]int64
	for i := 0; i < 13; i++ {
		_, _ = d.Pool.Exec(ctx, `SELECT pg_stat_force_next_flush()`)
		time.Sleep(300 * time.Millisecond)
		last = indexScans(t, d)
		if until(last) {
			return last
		}
	}
	return last
}

func TestAnalyticsIndexes_TheEngineOnlyScansOneOfTheThree0009Declares_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	// ── THE CENSUS, BOTH DIRECTIONS. An index that vanished, or a fourth that appeared, must fail
	// here rather than leave this file measuring a population that no longer matches the schema.
	present := map[string]bool{}
	rows, err := d.Pool.Query(ctx,
		`SELECT indexname FROM pg_indexes WHERE tablename = 'issues' AND indexname LIKE 'idx_issues_%'`)
	if err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		present[n] = true
	}
	rows.Close()
	for name, shape := range declaredIn0009 {
		if !present[name] {
			t.Fatalf("migrations/0009_analytics.sql no longer creates %q (declared for %q). Either "+
				"it was dropped — in which case delete it from declaredIn0009 and from "+
				"scannedByTheEngine — or the migration broke.", name, shape)
		}
	}

	// ── THE FIXTURE IS MULTI-TENANT ON PURPOSE. See the file header: one workspace holding every
	// row makes a seq scan correct for every query and every index read as unused.
	const nWorkspaces, perWorkspace = 200, 300
	wss := d.Workspaces(t, nWorkspaces)
	subject := wss[0]

	var wsIDs, teamIDs []string
	for _, w := range wss {
		wsIDs = append(wsIDs, w.ID)
		teamIDs = append(teamIDs, d.Team(t, w.ID).ID)
	}

	var subjectMembers []string
	for i := 0; i < 40; i++ {
		var id string
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO members (workspace_id, name, email) VALUES ($1, $2, $3) RETURNING id`,
			subject.ID, fmt.Sprintf("Member %d", i), fmt.Sprintf("m%d@example.com", i)).Scan(&id); err != nil {
			t.Fatalf("seed member: %v", err)
		}
		subjectMembers = append(subjectMembers, id)
	}
	others := map[string]string{}
	for _, w := range wss[1:] {
		var id string
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO members (workspace_id, name, email) VALUES ($1, 'Other', $2) RETURNING id`,
			w.ID, "other@"+w.ID+".example").Scan(&id); err != nil {
			t.Fatalf("seed other member: %v", err)
		}
		others[w.ID] = id
	}

	for i := range wsIDs {
		assignees := subjectMembers
		if i > 0 {
			assignees = []string{others[wsIDs[i]]}
		}
		if _, err := d.Pool.Exec(ctx, `
        INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
                            assignee_id, creator_id, due_date, completed_at, ai_cost_usd,
                            created_at, updated_at)
        SELECT $1, $2, g, 'IDX-' || g, 'issue ' || g,
               (ARRAY['backlog','todo','in_progress','in_review','done','cancelled'])[1 + (g % 6)],
               g % 4,
               ($3::text[])[1 + (g % array_length($3::text[], 1))],
               'idxprobe',
               CASE WHEN g % 3 = 0 THEN NOW() - make_interval(days => (g % 90)) ELSE NULL END,
               CASE WHEN g % 6 IN (4,5) THEN NOW() - make_interval(days => (g % 60)) ELSE NULL END,
               CASE WHEN g % 5 = 0 THEN (g % 100)::float / 10 ELSE 0 END,
               NOW() - make_interval(days => (g % 120)),
               NOW() - make_interval(days => (g % 120))
          FROM generate_series(1, $4::int) g`,
			wsIDs[i], teamIDs[i], assignees, perWorkspace); err != nil {
			t.Fatalf("seed issues for workspace %d: %v", i, err)
		}
	}
	if _, err := d.Pool.Exec(ctx, `ANALYZE issues`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	// ── THE WARM-UP IS ALSO THE PROOF THAT idx_issues_due IS REACHABLE AT ALL. This is a query of
	// exactly the shape 0009 declares for it, and it DOES use the index. Without this, "the engine
	// never scans it" could equally mean the index is unusable, mis-declared, or excluded by the
	// planner for a reason that has nothing to do with the engine's SQL.
	var overdue int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM issues
          WHERE workspace_id = $1 AND due_date IS NOT NULL AND due_date < NOW()
            AND status NOT IN ('done','cancelled')`, subject.ID).Scan(&overdue); err != nil {
		t.Fatalf("warm-up overdue count: %v", err)
	}
	if overdue == 0 {
		t.Fatalf("[A-PREMISE] the fixture produced no overdue-and-open issues, so idx_issues_due " +
			"has nothing to be used FOR and every result below is vacuous")
	}

	baseline := flushedScans(t, d, func(s map[string]int64) bool { return s["idx_issues_due"] > 0 })
	if baseline["idx_issues_due"] == 0 {
		t.Fatalf("[A-REACHABLE] a query of exactly the shape 0009 declares for idx_issues_due did "+
			"NOT scan it (%d overdue rows seeded). Until that holds, 'the engine never scans it' "+
			"is not a statement about the engine — the index may simply be unusable here.", overdue)
	}
	t.Logf("REACHABLE (premise held): idx_issues_due IS reachable — a query of its declared shape scanned it "+
		"(%d overdue-and-open rows in the subject workspace)", overdue)

	// ── DRIVE EVERY EXPORTED ENGINE METHOD. "Unused" must not be able to mean "I did not call it".
	e := analytics.New(d.Pool)
	teamID := teamIDs[0]
	calls := []struct {
		name string
		run  func() error
	}{
		{"GetVelocity", func() error { _, err := e.GetVelocity(ctx, teamID, subject.ID, 6); return err }},
		{"GetDistribution/status", func() error {
			_, err := e.GetDistribution(ctx, subject.ID, "status", 30)
			return err
		}},
		{"GetDistribution/label", func() error {
			_, err := e.GetDistribution(ctx, subject.ID, "label", 30)
			return err
		}},
		{"GetDistribution/priority", func() error {
			_, err := e.GetDistribution(ctx, subject.ID, "priority", 30)
			return err
		}},
		{"GetTimeToResolution", func() error {
			_, err := e.GetTimeToResolution(ctx, subject.ID, teamID, 30)
			return err
		}},
		{"GetAICostTrends", func() error { _, err := e.GetAICostTrends(ctx, subject.ID, 30); return err }},
		{"GetWorkload", func() error { _, err := e.GetWorkload(ctx, subject.ID, ""); return err }},
		{"GetWorkload/team", func() error { _, err := e.GetWorkload(ctx, subject.ID, teamID); return err }},
		{"ExportVelocityCSV", func() error { return e.ExportVelocityCSV(ctx, teamID, subject.ID, 6, io.Discard) }},
		{"ExportAICostTrendsCSV", func() error { return e.ExportAICostTrendsCSV(ctx, subject.ID, 30, io.Discard) }},
		{"ExportDistributionCSV", func() error {
			return e.ExportDistributionCSV(ctx, subject.ID, "status", 30, io.Discard)
		}},
	}
	for _, c := range calls {
		if err := c.run(); err != nil {
			t.Fatalf("[A-DROVE] %s returned %v — a method that errored did not exercise its SQL, "+
				"so its indexes would read as unused for the wrong reason", c.name, err)
		}
	}

	after := flushedScans(t, d, func(s map[string]int64) bool {
		return s["idx_issues_workspace"] > baseline["idx_issues_workspace"]+5
	})

	// ── THE INSTRUMENT'S OWN PRECONDITION. If no counter moved, every zero below is a fact about
	// the stats plumbing, not about the engine, and nothing may be concluded.
	var moved []string
	for name, n := range after {
		if n > baseline[name] {
			moved = append(moved, fmt.Sprintf("%s+%d", name, n-baseline[name]))
		}
	}
	sort.Strings(moved)
	if len(moved) == 0 {
		t.Fatalf("[A-INSTRUMENT] not one index counter moved while %d engine methods ran. Every "+
			"zero here would be uninterpretable.", len(calls))
	}
	t.Logf("INSTRUMENT live — counters that moved: %v", moved)

	// ── THE PINNED RESULT.
	for name, wantScanned := range scannedByTheEngine {
		gotScanned := after[name] > baseline[name]
		if gotScanned == wantScanned {
			continue
		}
		if wantScanned {
			t.Errorf("[A-CLAIM/%s] this index WAS scanned by the engine when the measurement was "+
				"taken and now is not (%d → %d). A query rewrite lost it; 0009 declares it for %q.",
				name, baseline[name], after[name], declaredIn0009[name])
			continue
		}
		t.Errorf("[A-CLAIM/%s] this index is now scanned by the engine (%d → %d) and this file "+
			"pins it as UNUSED. That is good news — 0009 declares it for %q and the engine has "+
			"started running that shape. Flip scannedByTheEngine[%q] to true and delete the "+
			"finding from the queue.",
			name, baseline[name], after[name], declaredIn0009[name], name)
	}

	for name, want := range scannedByTheEngine {
		if !want {
			t.Logf("MEASURED UNUSED: %s (0009 declares it for %q) — %d → %d",
				name, declaredIn0009[name], baseline[name], after[name])
		}
	}
}
