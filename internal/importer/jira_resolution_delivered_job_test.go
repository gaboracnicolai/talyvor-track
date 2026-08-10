package importer_test

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// jira_resolution_delivered_job_test.go — the same rule driven END TO END on real Postgres, through
// the async runner and a jira_csv job, and read back out of BOTH channels an operator has: the
// `issues` table and the job row's `warnings` TEXT[].
//
// ⚠ THE UNIT TEST CANNOT SEE THE THING THIS MERGE IS ABOUT. What changed is what the REPORT says,
// and the report an operator reads is not ImportResult — it is migration 0026's `warnings` column,
// rendered by renderWarnings, which GROUPS notes and can therefore turn a per-row silence into a
// missing line, a merged line or an unchanged one. #80 exists because that renderer had its own
// defect. So the assertion below is on the STRING LIST that lands in Postgres.
//
// ⚠ AND THE ROWS ARE ASSERTED TOO, FOR THE OPPOSITE REASON. The claim that makes this a session call
// rather than #82's deferred decision is "no data moves". A warnings-only assertion cannot see a
// status that moved; a status assertion cannot see a warning that vanished. Both, or neither means
// anything.
//
// The fixture is the measured export's shape — Status "Closed" on every resolved row, because on the
// real instance it always is — and carries one row of each class, so a change that collapsed the
// classifier to a single answer fails somewhere here.
const jiraCSVDeliveredRows = "Summary,Description,Status,Priority,Resolution,Resolved\n" +
	"Fixed row,d,Closed,High,Fixed,06/Aug/2026 8:06 PM\n" +
	"Rejected row,d,Closed,High,Rejected,15/Jul/2026 2:34 PM\n" +
	"Abandoned row,d,Closed,High,Won't Fix,23/Mar/2026 4:59 PM\n"

func TestJobRow_JiraCSV_FixedIsNotReportedAndStillLandsDelivered(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(jiraCSVDeliveredRows))
	if err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	j, err := js.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	// PREMISE, asserted before anything is read off it. A job that did not import all three rows
	// makes every absence below vacuously true — "no warning about Fixed" is satisfied perfectly by
	// an import that never saw a Fixed row.
	if j.Status != importer.JobSucceeded || j.Imported != 3 || j.Failed != 0 || j.Skipped != 0 {
		t.Fatalf("PREMISE: job row = {status:%q imported:%d failed:%d skipped:%d}, want {succeeded 3 0 0}",
			j.Status, j.Imported, j.Failed, j.Skipped)
	}

	// ── channel 1: the rows. Nothing this merge does may move one. ──
	got := readIssueStatusByTitle(t, d, ws.ID)
	for _, tc := range []struct {
		title       string
		wantStatus  string
		wantHasDate bool
		why         string
	}{
		{"Fixed row", "done", true,
			`Jira resolved it "Fixed" — delivered work, and this merge changes only whether it is reported`},
		{"Rejected row", "done", true,
			`Jira resolved it "Rejected", which Track still refuses to interpret — #82's decision, untouched`},
		{"Abandoned row", "cancelled", false,
			`Jira resolved it "Won't Fix" — Track's own vocabulary reads that as cancellation`},
	} {
		row, ok := got[tc.title]
		if !ok {
			t.Errorf("%q is not in the issues table at all", tc.title)
			continue
		}
		if row.status != tc.wantStatus {
			t.Errorf("%q: status column = %q, want %q — %s", tc.title, row.status, tc.wantStatus, tc.why)
		}
		if row.hasComplete != tc.wantHasDate {
			t.Errorf("%q: completed_at IS NOT NULL = %v, want %v — %s", tc.title, row.hasComplete, tc.wantHasDate, tc.why)
		}
	}

	// ── channel 2: the warnings TEXT[] the operator actually reads ──
	var fixedLine, rejectedLine, abandonedLine string
	for _, w := range j.Warnings {
		switch {
		case strings.Contains(w, `resolution "Fixed"`):
			fixedLine = w
		case strings.Contains(w, `resolution "Rejected"`):
			rejectedLine = w
		case strings.Contains(w, `resolution "Won't Fix"`):
			abandonedLine = w
		}
	}
	// PREMISE for the absence: the other two lines must be present, or an empty warnings column —
	// a renderer that stopped rendering, a job row that never got them — would read as a fix.
	if rejectedLine == "" {
		t.Fatalf("PREMISE: no line about the Rejected row in %#v — the report is not being written at "+
			"all, so the absence asserted below would mean nothing", j.Warnings)
	}
	if abandonedLine == "" {
		t.Fatalf("PREMISE: no line about the Won't Fix row in %#v", j.Warnings)
	}
	if fixedLine != "" {
		t.Errorf("the job's warnings still carry a line about the Fixed row: %q\n"+
			"On the measured Cloud instance that line stood for 19,698 of 29,512 resolved issues "+
			"(66.7%%) and said Track could not tell whether delivered work was delivered.\nfull report: %#v",
			fixedLine, j.Warnings)
	}
}
