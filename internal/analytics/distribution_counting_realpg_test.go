package analytics_test

// THE DISTRIBUTION REPORT'S WORKSPACE SCOPE COULD BE NEUTRALISED WITH EVERY TEST IN THE REPOSITORY
// GREEN, AND FOUR MORE OF ITS RULES WERE UNASSERTED WITH IT.
//
// Third in the series after #152 (workload) and #153 (velocity), and the same cause each time:
// analytics.GetDistribution's rows are chosen and counted ENTIRELY IN SQL — a workspace scope, a
// window predicate, a GROUP BY built from allowedGroupBy, a SUM, and a separate UNNEST path for
// labels — while the two tests that named it are pgxmock tests that FEED the buckets
// (`("backlog",15,0.0)`, `("done",25,12.50)`) and assert the Go-side percentage over them.
//
// MEASURED, NOT INFERRED, at 9573bb3 — one term of the shipped code mutated at a time, each run over
// the whole analytics package, each restored in a `finally` and sha256-verified
// (scripts/w34-distribution-counting-controls-8f5c.py):
//
//	D1  the window keys on updated_at instead of created_at    another cohort     NOT CAUGHT
//	D2  the assignee group loses its COALESCE                  no unassigned row  NOT CAUGHT
//	D3  THE WORKSPACE SCOPE NEUTRALISED                        every workspace    NOT CAUGHT
//	D4  the label path loses its window                        another cohort     NOT CAUGHT
//	D6  SUM(ai_cost_usd) -> SUM(ai_tokens)                     a wrong money row  NOT CAUGHT
//
// ⚠ D3 IS THE ONE TO READ. `WHERE workspace_id = $1` is the only thing standing between this report
// and every other tenant's issue counts, and nothing in the repository asserts it: scope_read_test.go
// covers velocity and burndown, authz_refusal_sweep_test.go asserts that each route refuses a caller
// with NO authorized workspace (a different question from whether an authorized one is filtered),
// and the .semgrep workspace-authz lock is about handlers reading a spoofable workspace id, not about
// a predicate inside a statement. The mutation used here is `workspace_id = ANY(ARRAY[$1,
// workspace_id])` rather than a deletion, deliberately: #153 measured that replacing the text an
// ExpectQuery regex matches reds the mock tests for a reason that is not an assertion, so a control
// that changes rows must leave the matched substring alone. This one does, and it scored green.
//
// With this file in place all five are CAUGHT, by it and by nothing else. D7 (clampDays' ceiling)
// is the must-stay-green companion: CAUGHT by the pre-existing TestClampDays_BoundsRespected and NOT
// by this file, which is what stops this file reading as a catch-all. D5 (the pct denominator) is
// caught by BOTH — the Go-side arithmetic is the half a mock CAN see, and this file asserts it too,
// so it justifies neither on its own and is recorded that way rather than claimed as a catch. Two
// VOID controls (ORDER BY rewritten to the ordinal, the priority cast doubled) score NOT CAUGHT as
// required.
//
// ⚠ WHAT THIS FILE PINS AND DOES NOT ENDORSE — `pct` ANSWERS TWO DIFFERENT QUESTIONS UNDER ONE NAME,
// AND THE CSV COLUMN IS CALLED `pct` EITHER WAY. For status/priority/assignee/team the denominator is
// the number of ISSUES in the cohort. For `group_by=label` the rows are UNNESTed label applications,
// so an issue carrying three labels is counted three times and an issue carrying NONE is in no bucket
// at all and in no denominator either. Pinned below as numbers: the same cohort of 10 issues renders
// a status report whose counts sum to 10 and a label report whose counts sum to 5, over 3 issues.
// Whether an unlabelled issue deserves a bucket is a product decision; it is measured here so it
// cannot change silently.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// seedDistIssue writes one issue with an explicit created_at/updated_at pair, because the window
// predicate is the thing under test and a row whose two timestamps agree cannot tell which one the
// query read. Both are SQL rather than Go times so the fixture and the query share one clock.
func seedDistIssue(t *testing.T, d *testutil.DB, wsID, teamID string, n int, status, createdSQL, updatedSQL string, assignee *string, labels []string, cost float64) {
	t.Helper()
	_, err := d.Pool.Exec(context.Background(), `
        INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
                            assignee_id, creator_id, labels, ai_cost_usd, ai_tokens, created_at, updated_at)
        VALUES ($1, $2, $3::int, 'DS-' || $3::int, 'dist ' || $3::int, $4, 0, $5, 'dsprobe', $6, $7,
                777777, `+createdSQL+`, `+updatedSQL+`)`,
		wsID, teamID, n, status, assignee, labels, cost)
	if err != nil {
		t.Fatalf("seed issue %d (%s): %v", n, status, err)
	}
}

func distBuckets(t *testing.T, out []analytics.DistributionBucket) map[string]analytics.DistributionBucket {
	t.Helper()
	m := map[string]analytics.DistributionBucket{}
	for _, b := range out {
		m[b.Label] = b
	}
	return m
}

func TestGetDistribution_TheSQLsOwnCountingRules_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	wsA := d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	wsB := d.Workspace(t)
	teamB := d.Team(t, wsB.ID)

	alice := seedWorkloadMember(t, d, wsA.ID, "Alice", "alice-dist@example.com")

	const inWindow = "NOW() - INTERVAL '5 days'"
	const longAgo = "NOW() - INTERVAL '200 days'"
	const now = "NOW()"

	// ── WORKSPACE A, INSIDE THE 30-DAY WINDOW. Ten issues, four statuses, and every expected count
	// (4/3/2/1) and every expected cost (4.00/6.00/1.00/0.00) distinct, so no mutation of one term
	// can land on another's expected value by luck. Three are ASSIGNED and seven are not — the shape
	// every imported issue has, since no transport maps Assignee.
	for i := 1; i <= 4; i++ {
		seedDistIssue(t, d, wsA.ID, teamA.ID, i, "backlog", inWindow, now, nil, []string{}, 1.00)
	}
	seedDistIssue(t, d, wsA.ID, teamA.ID, 5, "todo", inWindow, now, &alice, []string{"bug", "ui"}, 2.00)
	seedDistIssue(t, d, wsA.ID, teamA.ID, 6, "todo", inWindow, now, &alice, []string{"bug", "ui"}, 2.00)
	seedDistIssue(t, d, wsA.ID, teamA.ID, 7, "todo", inWindow, now, &alice, []string{"bug"}, 2.00)
	seedDistIssue(t, d, wsA.ID, teamA.ID, 8, "in_progress", inWindow, now, nil, []string{}, 0.50)
	seedDistIssue(t, d, wsA.ID, teamA.ID, 9, "in_progress", inWindow, now, nil, []string{}, 0.50)
	seedDistIssue(t, d, wsA.ID, teamA.ID, 10, "done", inWindow, now, nil, []string{}, 0.00)

	// ── WORKSPACE A, OUTSIDE THE WINDOW BUT TOUCHED TODAY. This is the ordinary shape of imported
	// history: created years ago, written by the import a moment ago. A window that keys on
	// updated_at pulls all five in under a status and a label that exist nowhere else.
	for i := 11; i <= 15; i++ {
		seedDistIssue(t, d, wsA.ID, teamA.ID, i, "wsA_out_of_window", longAgo, now, nil, []string{"stale"}, 100.00)
	}

	// ── WORKSPACE B, INSIDE THE WINDOW. The cross-tenant canary: its status and label strings occur
	// nowhere in workspace A, so their presence is unambiguous and their absence is the scope working.
	for i := 1; i <= 6; i++ {
		seedDistIssue(t, d, wsB.ID, teamB.ID, i, "wsB_canary", inWindow, now, nil, []string{"wsB_label"}, 999.00)
	}

	e := analytics.New(d.Pool)

	// ── BY STATUS.
	byStatus, err := e.GetDistribution(ctx, wsA.ID, "status", 30)
	if err != nil {
		t.Fatalf("GetDistribution(status): %v", err)
	}
	st := distBuckets(t, byStatus)
	if _, leaked := st["wsB_canary"]; leaked {
		t.Fatalf("[D-TENANCY] workspace B's issues are in workspace A's distribution report — the "+
			"workspace predicate is not filtering: %+v", byStatus)
	}
	if _, stale := st["wsA_out_of_window"]; stale {
		t.Errorf("[D-WINDOW] an issue CREATED 200 days ago and updated today is inside a 30-day "+
			"report — the window is on created_at, and imported history is exactly this row: %+v", byStatus)
	}
	if len(byStatus) != 4 {
		t.Fatalf("[D-BUCKETS] got %d status buckets, want 4 (backlog, todo, in_progress, done): %+v",
			len(byStatus), byStatus)
	}
	if byStatus[0].Label != "backlog" || byStatus[0].Count != 4 {
		t.Errorf("[D-ORDER] the first bucket is %q with %d, want backlog with 4 — buckets are ordered "+
			"by count descending", byStatus[0].Label, byStatus[0].Count)
	}
	for label, want := range map[string]int{"backlog": 4, "todo": 3, "in_progress": 2, "done": 1} {
		if st[label].Count != want {
			t.Errorf("[D-COUNT] status %q count = %d, want %d", label, st[label].Count, want)
		}
	}
	for label, want := range map[string]float64{"backlog": 4.00, "todo": 6.00, "in_progress": 1.00, "done": 0.00} {
		if st[label].AICostUSD != want {
			t.Errorf("[D-COST] status %q ai_cost_usd = %v, want %v — the money column is ai_cost_usd, "+
				"and every row in this fixture carries 777777 ai_tokens so a column swap is loud",
				label, st[label].AICostUSD, want)
		}
	}
	if p := st["backlog"].Pct; p < 0.399 || p > 0.401 {
		t.Errorf("[D-PCT] backlog pct = %v, want 0.4 — the denominator is the 10 issues of the "+
			"cohort", p)
	}

	// ── BY ASSIGNEE. Seven of the ten are unassigned, which is what an imported backlog is; without
	// the COALESCE they group under NULL and the report either loses them or fails to scan.
	byAssignee, err := e.GetDistribution(ctx, wsA.ID, "assignee", 30)
	if err != nil {
		t.Fatalf("[D-ASSIGNEE] GetDistribution(assignee) returned an error: %v — an unassigned issue "+
			"must land in a NAMED bucket rather than a NULL the scan cannot read", err)
	}
	as := distBuckets(t, byAssignee)
	if as["unassigned"].Count != 7 {
		t.Errorf("[D-ASSIGNEE] the 'unassigned' bucket holds %d, want 7 — every imported issue is "+
			"unassigned, so this bucket is the whole report for an imported workspace: %+v",
			as["unassigned"].Count, byAssignee)
	}
	if as[alice].Count != 3 {
		t.Errorf("[D-ASSIGNEE] Alice's bucket holds %d, want 3 (keyed by member id, which is what the "+
			"column carries): %+v", as[alice].Count, byAssignee)
	}

	// ── BY LABEL — a different query with its own workspace scope and its own window.
	byLabel, err := e.GetDistribution(ctx, wsA.ID, "label", 30)
	if err != nil {
		t.Fatalf("GetDistribution(label): %v", err)
	}
	lb := distBuckets(t, byLabel)
	if _, leaked := lb["wsB_label"]; leaked {
		t.Fatalf("[D-TENANCY-LABEL] workspace B's label is in workspace A's label report — the UNNEST "+
			"path carries its own scope and it is not filtering: %+v", byLabel)
	}
	if _, stale := lb["stale"]; stale {
		t.Errorf("[D-WINDOW-LABEL] the out-of-window label is in a 30-day label report — the UNNEST "+
			"path carries its own window and the two must agree: %+v", byLabel)
	}
	if lb["bug"].Count != 3 || lb["ui"].Count != 2 {
		t.Errorf("[D-LABEL] bug = %d and ui = %d, want 3 and 2 — an issue carrying two labels is "+
			"counted under both: %+v", lb["bug"].Count, lb["ui"].Count, byLabel)
	}

	// ── THE PINNED AMBIGUITY, AS NUMBERS. One cohort, two reports, two denominators under one field
	// name — and the CSV export writes the column as `pct` for both.
	statusTotal, labelTotal := 0, 0
	for _, b := range byStatus {
		statusTotal += b.Count
	}
	for _, b := range byLabel {
		labelTotal += b.Count
	}
	if statusTotal != 10 {
		t.Errorf("[D-DENOMINATOR] the status report's counts sum to %d, want 10 — one row per issue",
			statusTotal)
	}
	if labelTotal != 5 {
		t.Errorf("[D-DENOMINATOR] the label report's counts sum to %d, want 5 — one row per label "+
			"APPLICATION over the 3 labelled issues, not per issue", labelTotal)
	}
	if p := lb["bug"].Pct; p < 0.599 || p > 0.601 {
		t.Errorf("[D-DENOMINATOR] bug pct = %v, want 0.6 (3 of 5 label applications). It is NOT 0.3, "+
			"which is the share of the 10 issues carrying it — the same field name answers a "+
			"different question on this path, and 7 unlabelled issues are in no bucket and no "+
			"denominator", p)
	}
	t.Logf("PINNED (measured, not endorsed): one cohort of 10 issues renders a status report whose "+
		"counts sum to %d and a label report whose counts sum to %d over 3 labelled issues; `pct` is "+
		"share-of-issues on one and share-of-label-applications on the other, and the CSV column is "+
		"`pct` for both.", statusTotal, labelTotal)
}
