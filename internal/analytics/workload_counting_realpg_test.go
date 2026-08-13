package analytics_test

// A TEST NAMED FOR COUNTING COUNTED NOTHING, AND EVERY COUNTING RULE OF THE WORKLOAD REPORT WAS
// UNASSERTED.
//
// analytics.GetWorkload's four numbers are computed ENTIRELY IN SQL — three `COUNT(*) FILTER (...)`
// predicates and a SUM, over a JOIN, under a workspace/team scope. The only test that named them,
// engine_test.go's TestGetWorkload_CountsOpenAndOverdueCorrectly (renamed by this merge to
// ...ScansEachRowIntoMemberWorkload), is a pgxmock test: it FEEDS the
// row `("alice", 7, 3, 2, 1.50)` and asserts the struct carries 2. It asserts that rows are scanned
// into fields, which is worth having and is not what its name claims.
//
// MEASURED, NOT INFERRED, at 7abefbe — five one-predicate mutations of the shipped SQL, each run
// over the whole analytics package, each restored in a `finally` and sha256-verified
// (scripts/w34-workload-counting-controls-4c8e.py):
//
//	M1  `i.due_date < NOW()`      -> `> NOW()`                    overdue inverted     NOT CAUGHT
//	M2  in_progress FILTER drops 'in_review'                      in_progress halved   NOT CAUGHT
//	M3  open FILTER stops excluding 'cancelled'                   cancelled reads open NOT CAUGHT
//	M4  overdue FILTER stops excluding done/cancelled             done work "overdue"  NOT CAUGHT
//	M5  the team scope neutralised                                every team's rows    NOT CAUGHT
//
// Five for five green. A mock cannot see a predicate it supplies the answer to, so the rules are
// asserted HERE, against real Postgres, through the shipped method.
//
// ⚠ WHAT THIS FILE PINS AND DOES NOT ENDORSE — TWO PROPERTIES, BOTH MEASURED, BOTH DECISIONS:
//
//  1. THE JOIN IS INNER, SO UNASSIGNED WORK IS INVISIBLE — and every imported issue is unassigned.
//     No import transport maps Assignee (see importer/linear_csv_due_date.go, which measured the
//     same thing from the other end), so a workspace whose backlog arrived from Jira or Linear
//     renders an EMPTY workload report while holding overdue work. Pinned below as a number: the
//     rows this report can see are fewer than the rows the workspace holds.
//  2. A DATE-ONLY DUE DATE IS OVERDUE FROM 00:00 UTC. Both providers' due dates are date-only
//     (Linear's scalar is literally `TimelessDate`), so an import stores midnight, and
//     `due_date < NOW()` reports an issue due TODAY as overdue for all of today. Whether "due" means
//     start or end of day — and in whose timezone — is a product decision; it is measured here so it
//     cannot change silently in either direction.
//
// ⚠ THE MIDNIGHT CASE IS COMPUTED, NOT ASSUMED. Its expectation is read from the DATABASE
// (`NOW() > date_trunc('day', NOW())`) rather than hardcoded, so a run that starts at exactly
// 00:00:00 UTC asserts the other answer instead of flaking.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// seedWorkloadIssue writes one issue with the exact status/due-date/assignee shape a case needs.
// due is SQL, not a Go time, so the fixture and the query share one clock: a test that computes
// "overdue" from the test process's wall clock and asserts it against Postgres's NOW() is measuring
// clock skew as well as the predicate.
func seedWorkloadIssue(t *testing.T, d *testutil.DB, wsID, teamID string, n int, status, dueSQL string, assignee *string, cost float64) {
	t.Helper()
	_, err := d.Pool.Exec(context.Background(), `
        INSERT INTO issues (workspace_id, team_id, number, identifier, title, status, priority,
                            assignee_id, creator_id, due_date, ai_cost_usd)
        VALUES ($1, $2, $3::int, 'WL-' || $3::int, 'workload ' || $3::int, $4, 0, $5, 'wlprobe', `+dueSQL+`, $6)`,
		wsID, teamID, n, status, assignee, cost)
	if err != nil {
		t.Fatalf("seed issue %d (%s, due %s): %v", n, status, dueSQL, err)
	}
}

func seedWorkloadMember(t *testing.T, d *testutil.DB, wsID, name, email string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO members (workspace_id, name, email) VALUES ($1, $2, $3) RETURNING id`,
		wsID, name, email).Scan(&id); err != nil {
		t.Fatalf("seed member %s: %v", name, err)
	}
	return id
}

func TestGetWorkload_TheSQLsOwnCountingRules_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	ws := d.Workspace(t)
	teamA := d.Team(t, ws.ID)
	teamB := d.Team(t, ws.ID)

	alice := seedWorkloadMember(t, d, ws.ID, "Alice", "alice@example.com")
	carol := seedWorkloadMember(t, d, ws.ID, "Carol", "carol@example.com")
	bob := seedWorkloadMember(t, d, ws.ID, "Bob", "bob@example.com")

	// ── ALICE, TEAM A. Every number below is distinct from every other number in this fixture, so
	// no mutation of one predicate can land on another predicate's expected value by luck.
	//	open        = 4   (backlog, todo, in_progress, in_review)
	//	in_progress = 2   (in_progress, in_review)
	//	overdue     = 2   (the two past-due OPEN ones)
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 1, "backlog", "NULL", &alice, 0.25)
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 2, "todo", "NOW() + INTERVAL '10 days'", &alice, 0.25)
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 3, "in_progress", "NOW() - INTERVAL '5 days'", &alice, 0.25)
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 4, "in_review", "NOW() - INTERVAL '3 days'", &alice, 0.25)
	// done and cancelled: neither is open, and the done one is PAST ITS DUE DATE — the row that
	// separates "overdue" from "has a due date in the past".
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 5, "done", "NOW() - INTERVAL '20 days'", &alice, 1.0)
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 6, "cancelled", "NULL", &alice, 1.0)

	// ── ALICE, TEAM B — the same member, another team. The team-scoped call must not see these and
	// the workspace-wide call must.
	seedWorkloadIssue(t, d, ws.ID, teamB.ID, 7, "backlog", "NULL", &alice, 0)
	seedWorkloadIssue(t, d, ws.ID, teamB.ID, 8, "backlog", "NULL", &alice, 0)
	seedWorkloadIssue(t, d, ws.ID, teamB.ID, 9, "backlog", "NULL", &alice, 0)

	// ── BOB is a TEAM B member only: he must be ABSENT from the team-A report entirely, which is a
	// different failure from being present with a zero.
	seedWorkloadIssue(t, d, ws.ID, teamB.ID, 10, "todo", "NULL", &bob, 0)
	seedWorkloadIssue(t, d, ws.ID, teamB.ID, 11, "todo", "NULL", &bob, 0)

	// ── CAROL, TEAM A — the midnight boundary, held on its own member so it cannot disturb Alice's
	// numbers. `date_trunc('day', NOW())` is the shape EVERY import writes: both providers' due
	// dates are date-only, so the stored instant is 00:00 of the due day.
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 12, "todo", "date_trunc('day', NOW())", &carol, 0)

	// ── UNASSIGNED, TEAM A — open, overdue, and invisible to this report. This is the shape of
	// every imported issue.
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 13, "todo", "NOW() - INTERVAL '9 days'", nil, 0)
	seedWorkloadIssue(t, d, ws.ID, teamA.ID, 14, "in_progress", "NOW() - INTERVAL '9 days'", nil, 0)

	var afterMidnight bool
	if err := d.Pool.QueryRow(ctx, `SELECT NOW() > date_trunc('day', NOW())`).Scan(&afterMidnight); err != nil {
		t.Fatalf("read the clock the fixture and the query share: %v", err)
	}
	wantCarolOverdue := 0
	if afterMidnight {
		wantCarolOverdue = 1
	}

	e := analytics.New(d.Pool)

	// ── TEAM-SCOPED.
	teamRows, err := e.GetWorkload(ctx, ws.ID, teamA.ID)
	if err != nil {
		t.Fatalf("GetWorkload(team A): %v", err)
	}
	byID := map[string]analytics.MemberWorkload{}
	for _, r := range teamRows {
		byID[r.MemberID] = r
	}
	if _, present := byID[bob]; present {
		t.Errorf("[A-SCOPE] Bob has issues in team B only and appears in the team-A report — the "+
			"team predicate is not reaching the JOIN (rows: %+v)", teamRows)
	}
	if len(teamRows) != 2 {
		t.Errorf("[A-SCOPE] team-A workload returned %d members, want 2 (Alice, Carol): %+v",
			len(teamRows), teamRows)
	}

	a := byID[alice]
	if a.OpenIssues != 4 {
		t.Errorf("[A-OPEN] Alice open = %d, want 4 — done and cancelled are not open work", a.OpenIssues)
	}
	if a.InProgress != 2 {
		t.Errorf("[A-INPROGRESS] Alice in_progress = %d, want 2 — in_review counts as in progress "+
			"and nothing else does", a.InProgress)
	}
	if a.Overdue != 2 {
		t.Errorf("[A-OVERDUE] Alice overdue = %d, want 2 — past due AND still open; the done issue "+
			"20 days past its due date is not overdue and the one due in 10 days is not either", a.Overdue)
	}
	if a.AICostUSD != 3.0 {
		t.Errorf("[A-COST] Alice ai_cost_usd = %v, want 3.0 — the SUM is over EVERY row of the "+
			"scope, including the done and cancelled ones the counters exclude", a.AICostUSD)
	}
	if a.Name != "Alice" {
		t.Errorf("[A-JOIN] Alice's row carries name %q — the members JOIN is what supplies it", a.Name)
	}

	c := byID[carol]
	if c.Overdue != wantCarolOverdue {
		t.Errorf("[A-MIDNIGHT] an issue due TODAY (due_date = 00:00 of today, the shape every import "+
			"writes) counted overdue = %d, want %d. `due_date < NOW()` makes a date-only due date "+
			"overdue from midnight; if this changed, the product decided something and this line is "+
			"where it is recorded", c.Overdue, wantCarolOverdue)
	}

	// ── WORKSPACE-WIDE (teamID ""): the same member, more rows, and Bob appears.
	wsRows, err := e.GetWorkload(ctx, ws.ID, "")
	if err != nil {
		t.Fatalf("GetWorkload(workspace): %v", err)
	}
	byIDWS := map[string]analytics.MemberWorkload{}
	for _, r := range wsRows {
		byIDWS[r.MemberID] = r
	}
	if got := byIDWS[alice].OpenIssues; got != 7 {
		t.Errorf("[A-UNSCOPED] Alice open across the workspace = %d, want 7 (4 in team A + 3 in "+
			"team B) — an unscoped call must widen, and a team-scoped one must not", got)
	}
	if got := byIDWS[bob].OpenIssues; got != 2 {
		t.Errorf("[A-UNSCOPED] Bob open across the workspace = %d, want 2", got)
	}

	// ── THE PINNED BLINDNESS, AS A NUMBER RATHER THAN A SENTENCE. Two open, past-due, UNASSIGNED
	// issues sit in team A. The report cannot see them because the JOIN is inner — and an imported
	// issue is unassigned by construction, so this is what a Jira/Linear import renders as.
	var openInTeamA, overdueInTeamA int
	if err := d.Pool.QueryRow(ctx, `
        SELECT COUNT(*) FILTER (WHERE status NOT IN ('done','cancelled')),
               COUNT(*) FILTER (WHERE due_date IS NOT NULL AND due_date < NOW()
                                  AND status NOT IN ('done','cancelled'))
          FROM issues WHERE workspace_id = $1 AND team_id = $2`,
		ws.ID, teamA.ID).Scan(&openInTeamA, &overdueInTeamA); err != nil {
		t.Fatalf("count team A's real open/overdue work: %v", err)
	}
	reportedOpen, reportedOverdue := 0, 0
	for _, r := range teamRows {
		reportedOpen += r.OpenIssues
		reportedOverdue += r.Overdue
	}
	if openInTeamA != reportedOpen+2 {
		t.Errorf("[A-BLINDSPOT] team A holds %d open issues and the workload report accounts for "+
			"%d; this file pins the gap at exactly the 2 UNASSIGNED ones. A different gap means the "+
			"fixture or the JOIN moved", openInTeamA, reportedOpen)
	}
	if overdueInTeamA != reportedOverdue+2 {
		t.Errorf("[A-BLINDSPOT] team A holds %d overdue issues and the workload report accounts for "+
			"%d; the 2 unassigned overdue issues are invisible to it", overdueInTeamA, reportedOverdue)
	}
	t.Logf("PINNED (measured, not endorsed): team A holds open=%d overdue=%d; the workload report "+
		"accounts for open=%d overdue=%d. The difference is unassigned work, which is what every "+
		"imported issue is.", openInTeamA, overdueInTeamA, reportedOpen, reportedOverdue)
}
