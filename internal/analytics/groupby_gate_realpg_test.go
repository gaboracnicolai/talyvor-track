package analytics_test

// THE SQL-COMPOSITION GATE ON THE DISTRIBUTION REPORT COULD BE DELETED WITH EVERY TEST IN THE
// REPOSITORY GREEN — INCLUDING THE ONE WHOSE OWN FAILURE MESSAGE SAYS "to prevent SQL injection".
//
// `group_by` is raw caller input. handler.go:105 reads it off the query string and hands it to
// GetDistribution; handler.go:235 does the same for the CSV export, which reaches the identical
// engine call through ExportDistributionCSV (engine.go:789). `allowedGroupBy` is the ONLY thing
// between that string and `fmt.Sprintf`, which interpolates it into BOTH the projection and the
// GROUP BY of a statement whose `WHERE workspace_id = $1` is this report's entire tenancy scope.
//
// MEASURED at 3672af1, one term mutated at a time over the WHOLE repository (`go test ./...`
// against real Postgres), membership decided by SET SUBTRACTION against the run's own baseline
// FAIL set rather than by an exit code, each mutation restored in a `finally` and sha256-verified
// (scripts/w34-groupby-gate-controls-5b91.py):
//
//	M6  the whole map lookup replaced (`col, ok := groupBy, true`)   CAUGHT — but see below
//	M7  THE GATE ALONE OPENED, every mapping left byte-identical     NOT CAUGHT
//
// ⚠ M6 IS WHY THIS FILE EXISTS AND M7 IS WHY IT IS NOT A DUPLICATE. M6 changes two things at once:
// the gate AND the mapping (`assignee` -> COALESCE(assignee_id,'unassigned'), `priority` ->
// priority::text). It was caught — by TestGetDistribution_TheSQLsOwnCountingRules_RealPG, which
// reds on the MAPPING half, because `SELECT assignee::text` names a column that does not exist.
// M7 leaves every mapped column byte-identical and only lets an UNMAPPED group_by fall through.
// Nothing in the repository moved. Had `assignee` mapped to a bare column name, M6 would have been
// green too.
//
// ⚠ WHY THE TWO TESTS THAT NAME THIS GATE CANNOT SEE IT. Both assert only that AN ERROR CAME BACK:
//
//	engine_test.go:141  TestGetDistribution_RejectsUnknownGroupBy — passes "haxxor; DROP TABLE
//	                    issues;--" to a pgxmock engine and asserts err != nil. With the gate open
//	                    the mock has no expectation registered for the resulting query, so pgxmock
//	                    itself errors. The assertion cannot distinguish "the gate refused" from
//	                    "the mock was never told about this query", and it never reaches SQL at all.
//	export_refusal_test.go:89  the distribution/bogus refusal — runs against a REAL pool, and with
//	                    the gate open Postgres refuses `SELECT bogus::text ... GROUP BY bogus`
//	                    because `bogus` is not a column. The route still answers 400 and the test
//	                    still passes, for a reason that has nothing to do with the gate.
//
// Both oracles are "Postgres would choke on this". THE INPUTS THAT MATTER ARE THE ONES IT WOULD
// NOT CHOKE ON, and no test used one. That is the hole this file closes: the gate is asserted on
// MEMBERSHIP of allowedGroupBy, with an input Postgres accepts, so the refusal cannot be supplied
// by the database.
//
// ⚠ THE PREMISE IS MEASURED HERE, NOT ASSERTED. Half 1 below executes the expression through the
// pool in the same two positions the report interpolates it into and requires it to SUCCEED and to
// return the OTHER workspace's id. If that half ever reds, the premise moved and the gate half
// stops meaning what this header says — which is the point of running it rather than claiming it.
// It is a restatement of the statement's SHAPE (a claim about Postgres), deliberately not a copy of
// the engine's SQL: engine.go's string is unexported, and a copy of it here would drift silently.
//
// ⚠ NOT FIXED HERE, MEASURED: handler.go:239 justifies answering 400 rather than 500 on the grounds
// that "the one error this call returns without touching the database is 'unsupported group_by',
// which is the caller's input". That is true only while the gate stands. With M7 applied the same
// arm reports a raw Postgres error to the caller as a 400 — a different claim, and its own merge.
//
// THE CONTROLS, 6/6, each predicted BEFORE the run, each over the whole repository
// (scripts/w34-groupby-gate-controls-5b91.py):
//
//	C1  THE DEFECT (M7)                          CAUGHT by this file and by NOTHING else
//	C2  C1 with this file deleted                NOT CAUGHT — the measured blindness on main
//	C3  C1 with HALF 2 removed outright          NOT CAUGHT — half 2 is what catches C1
//	C4  clampDays' ceiling 365 -> 3650           CAUGHT elsewhere, NOT by this file
//	C5  the assignee mapping loses its COALESCE  CAUGHT by the counting guard, NOT by this file
//	C6  a synonym key -> the same expression     NOT CAUGHT — a stated limit, see below
//
// ⚠ C3'S FIRST FORM SCORED A FALSE CATCH AND IS RECORDED IN THE HARNESS RATHER THAN REPLACED. It
// blinded only the `err == nil` arm and left the message assertion live, so `err.Error()` panicked
// on nil and the run reddened. A control that reds because the test CRASHED is not evidence that
// the test asserted anything.
//
// ⚠ C4'S PREDICTED CATCHER WAS WRONG, AND ITS OWN FINDING IS HANDED ON RATHER THAN TAKEN HERE:
// TestClampDays_BoundsRespected (engine_test.go:298) asserts `clampDays(99999) == maxWindowDays`,
// i.e. it compares the constant to ITSELF and stays green when the constant moves. What reds are
// the window-clamp WIRING tests. One merge per finding.
//
// ⚠ THE LIMIT THIS FILE DOES NOT COVER (C6): it pins that ONE non-member is refused. It does not
// pin allowedGroupBy's key SET, so a new key admitting a new column is invisible to it.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/testutil"
)

// crossWorkspaceGroupBy is not a key of allowedGroupBy, and it is a legal Postgres expression in
// both the projection and the GROUP BY. It reads every workspace id in the issues table, so the
// answer it produces is visibly outside the scope the report's own `WHERE workspace_id = $1`
// establishes — which is what makes "an error came back" the wrong oracle for this gate.
const crossWorkspaceGroupBy = `(SELECT string_agg(DISTINCT workspace_id::text, ',') FROM issues)`

func TestGetDistribution_TheGroupByGateIsKeyedOnMembershipNotOnPostgresChoking_RealPG(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspaces(t, 2)
	wsA, wsB := ws[0], ws[1]
	d.Issue(t, wsA.ID, d.Team(t, wsA.ID).ID)
	d.Issue(t, wsB.ID, d.Team(t, wsB.ID).ID)
	ctx := context.Background()
	e := analytics.New(d.Pool)

	// ── HALF 1: THE PREMISE. Postgres ACCEPTS this group_by, so a refusal cannot come from it.
	probe := fmt.Sprintf(
		`SELECT %s::text, COUNT(*) FROM issues WHERE workspace_id = $1 GROUP BY %s`,
		crossWorkspaceGroupBy, crossWorkspaceGroupBy)
	var label string
	var n int
	if err := d.Pool.QueryRow(ctx, probe, wsA.ID).Scan(&label, &n); err != nil {
		t.Fatalf("PREMISE MOVED: Postgres refused the expression this test is built on, so the "+
			"gate half below would pass for the same reason the existing tests do: %v\nstatement: %s",
			err, probe)
	}
	if !strings.Contains(label, wsB.ID) {
		t.Fatalf("PREMISE MOVED: the expression ran but did not read past workspace %s — label=%q, "+
			"want it to contain the other workspace's id %s. The gate half below is only meaningful "+
			"while an admitted group_by can answer outside the report's own scope.",
			wsA.ID, label, wsB.ID)
	}

	// ── HALF 2: THE GATE. Membership, not validity.
	out, err := e.GetDistribution(ctx, wsA.ID, crossWorkspaceGroupBy, 30)
	if err == nil {
		t.Fatalf("the group_by gate ADMITTED a value that is not a key of allowedGroupBy: "+
			"%d bucket(s) came back for group_by=%s on a report scoped to workspace %s, and half 1 "+
			"just measured that this expression reads every workspace in the table. buckets=%v",
			len(out), crossWorkspaceGroupBy, wsA.ID, out)
	}
	if !strings.Contains(err.Error(), "unsupported group_by") {
		t.Fatalf("the call failed, but not at the gate: %v. An error from the database is what the "+
			"two pre-existing tests already accept, and it is not evidence that the caller's string "+
			"was kept out of the SQL.", err)
	}

	// ── HALF 3: ANTI-VACUITY. "Refuse everything" would satisfy half 2 on its own.
	got, err := e.GetDistribution(ctx, wsA.ID, "status", 30)
	if err != nil {
		t.Fatalf("a MAPPED group_by must still be served: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("group_by=status returned no buckets for a workspace holding an issue — the gate " +
			"is refusing its own allowlist, which would satisfy half 2 while breaking the report")
	}
}
