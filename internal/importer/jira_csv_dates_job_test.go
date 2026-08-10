package importer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// jira_csv_dates_job_test.go — the same two columns driven END TO END on real Postgres, through the
// async runner and a jira_csv job, and read back OUT OF THE issues TABLE.
//
// ⚠ THIS IS NOT THE UNIT TEST TWICE, AND ON THIS TRANSPORT THAT IS NOT A GENERALITY. #74 found the
// importer's UPSERT naming `due_date` and not naming `completed_at` at all, so a perfectly mapped
// CompletedAt was thrown away by the SQL — and it fixed that ONE statement. When this file was
// written the CSV path did NOT go through the upsert (a CSV row carried no provider identifier, so
// run() wrote it with issue.Store.Create, a DIFFERENT INSERT) — and measured on ba5d90a that
// statement names `due_date` and DID NOT NAME `completed_at` EITHER. The same seam, its second copy.
//
// ⚠ THE ROUTING SENTENCE ABOVE IS NOW HISTORY, NOT A FACT ABOUT THIS FIXTURE, AND THAT IS WHY IT IS
// LEFT VISIBLE. jiraRowMapper reads the export's `Issue key` column, so a jira_csv job whose fixture
// CARRIES that column now takes the upsert. THIS file's fixture does not carry it, so these rows
// still take Create and this file still measures the statement it was written for — deliberately
// unchanged, so the second copy of the seam keeps its own guard.
// Every source-level assertion in jira_csv_dates_test.go can be green while this file is red.

// A four-row export in exactly the shape measured off a real Jira CSV (see the probe script), one
// row per outcome:
//
//	done      + both dates → both columns populated
//	cancelled + Resolved   → due only; the completion time refused AND reported
//	todo      + Due Date   → due only, nothing reported
//	todo      + neither    → both NULL, nothing reported
const jiraCSVWithDates = "Summary,Description,Status,Priority,Labels,Due Date,Resolved,Created,Updated\n" +
	"Shipped work,d,Closed,High,bug,19/Jan/2025 12:00 AM,25/Mar/2025 10:03 AM,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n" +
	"Abandoned work,d,Won't Do,High,bug,19/Jan/2025 12:00 AM,25/Mar/2025 10:03 AM,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n" +
	"Planned work,d,To Do,High,bug,19/Jan/2025 12:00 AM,,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n" +
	"Bare work,d,To Do,High,bug,,,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n"

type csvIssueDates struct {
	status      string
	dueDate     *time.Time
	completedAt *time.Time
}

func readIssueDatesByTitle(t *testing.T, d *testutil.DB, wsID string) map[string]csvIssueDates {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT title, status, due_date, completed_at FROM issues WHERE workspace_id = $1`, wsID)
	if err != nil {
		t.Fatalf("read back issues: %v", err)
	}
	defer rows.Close()
	out := map[string]csvIssueDates{}
	for rows.Next() {
		var title, status string
		var due, completed *time.Time
		if err := rows.Scan(&title, &status, &due, &completed); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[title] = csvIssueDates{status: status, dueDate: due, completedAt: completed}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

func TestJobRow_JiraCSV_DatesLandInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(jiraCSVWithDates))
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
	// Every row lands. A degraded FIELD is not a failed ROW — the distinction #72 built the
	// warnings column for, and it must survive this merge.
	if j.Status != importer.JobSucceeded || j.Imported != 4 || j.Failed != 0 {
		t.Fatalf("job = {status:%s imported:%d failed:%d}, want {succeeded 4 0}", j.Status, j.Imported, j.Failed)
	}

	got := readIssueDatesByTitle(t, d, ws.ID)
	if len(got) != 4 {
		t.Fatalf("rows in Postgres = %d, want 4", len(got))
	}

	const wantDue = "2025-01-19T00:00:00Z"
	for _, title := range []string{"Shipped work", "Abandoned work", "Planned work"} {
		r := got[title]
		if r.dueDate == nil {
			t.Errorf("%s: due_date IS NULL in Postgres; the export column held %q", title, "19/Jan/2025 12:00 AM")
			continue
		}
		if s := r.dueDate.UTC().Format(time.RFC3339); s != wantDue {
			t.Errorf("%s: due_date = %s, want %s", title, s, wantDue)
		}
	}
	if got["Bare work"].dueDate != nil {
		t.Errorf("Bare work: due_date = %v, want NULL — the export carried none", got["Bare work"].dueDate)
	}

	// THE COLUMN issue.Store.Create DOES NOT NAME. This is the assertion the mapper cannot satisfy.
	if r := got["Shipped work"]; r.completedAt == nil {
		t.Errorf("Shipped work: completed_at IS NULL in Postgres on a done row; the export held %q — "+
			"if the mapper tests pass and this does not, the INSERT is dropping it", "25/Mar/2025 10:03 AM")
	} else if s, want := r.completedAt.UTC().Format(time.RFC3339), "2025-03-25T10:03:00Z"; s != want {
		t.Errorf("Shipped work: completed_at = %s, want %s", s, want)
	}
	for _, title := range []string{"Abandoned work", "Planned work", "Bare work"} {
		if got[title].completedAt != nil {
			t.Errorf("%s: completed_at = %v on a %s row, want NULL",
				title, got[title].completedAt, got[title].status)
		}
	}

	// AND THE REFUSAL REACHES THE JOB ROW — the async path is the one a real import uses, so a fix
	// that stopped at ImportResult would be inert exactly here (0026's whole reason for existing).
	joined := strings.Join(j.Warnings, "\n")
	if !strings.Contains(joined, "25/Mar/2025 10:03 AM") || !strings.Contains(joined, "cancelled") {
		t.Errorf("the refused completion time is not on the job row; warnings =\n%s", joined)
	}
	if len(j.Warnings) != 1 {
		t.Errorf("warnings = %d lines, want exactly 1 (one refused resolution date):\n%s", len(j.Warnings), joined)
	}
}

// The other direction, so the two columns cannot pass by always being written: an export with
// NEITHER column imports cleanly, writes two NULLs, and says nothing. Without this, a mapper that
// stamped time.Now() into both would satisfy every assertion above.
func TestJobRow_JiraCSV_NoDateColumnsIsCleanAndSilent(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	// ⚠ `Created` IS PRESENT AND THAT IS THE ASYMMETRY, NOT AN OVERSIGHT. "No column, no loss" is
	// true of Due Date and Resolved, whose absence leaves a truthful NULL. It is FALSE of Created:
	// issues.created_at is `DEFAULT NOW()`, so an absent column leaves a WRONG non-null value and
	// jira_csv_created.go reports it. Supplying it here keeps this test about the two columns #78
	// shipped rather than silently re-deciding a neighbouring merge.
	const noDates = "Summary,Description,Status,Priority,Labels,Created,Updated\n" +
		"Shipped work,d,Closed,High,bug,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n"
	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(noDates))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := js.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != importer.JobSucceeded || j.Imported != 1 {
		t.Fatalf("job = {status:%s imported:%d}, want {succeeded 1}", j.Status, j.Imported)
	}
	if len(j.Warnings) != 0 {
		t.Errorf("warnings = %v, want none — an export with no date columns has lost nothing", j.Warnings)
	}
	got := readIssueDatesByTitle(t, d, ws.ID)
	if r := got["Shipped work"]; r.dueDate != nil || r.completedAt != nil {
		t.Errorf("columns = {due:%v completed:%v}, want both NULL", r.dueDate, r.completedAt)
	}
}
