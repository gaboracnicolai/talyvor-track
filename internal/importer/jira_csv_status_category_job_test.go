package importer_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// jira_csv_status_category_job_test.go — the finding driven END TO END on real Postgres through the
// ASYNC RUNNER, because that is the path a real bulk import takes and #91 measured that a fix at the
// synchronous entry point can leave it untouched: Runner.csvSourceFor calls newCSVSource itself.
// A test that only ever calls ImportJiraCSV cannot tell the two apart.
//
// The four (Status, Status Category) pairs are the real ones from the 304-file corpus — see
// jira_csv_status_category_test.go's header for the whole-population numbers.

const jiraCSVStatusCategoryJobBody = "Issue key,Summary,Description,Status,Priority,Status Category,Resolved\n" +
	"PROJ-1,Triaged in a custom workflow,d,New,High,To Do,\n" +
	"PROJ-2,Under test,d,Ready for Test,High,In Progress,\n" +
	"PROJ-3,Merged and shipped,d,merged to master,High,Done,23/Jul/2026 7:36 PM\n" +
	"PROJ-4,Abandoned in a Done category,d,Won't Do,High,Done,23/Jul/2026 7:36 PM\n"

func runJiraCSVStatusCategoryJob(t *testing.T, d *testutil.DB, body string) string {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	if _, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return ws.ID
}

// THE STATUS COLUMN IN POSTGRES, per identifier. Asserting a count would let one row's status stand
// in for another's; this says which issue got which answer — including the row that must NOT move.
func TestJobRow_JiraCSV_StatusCategoryResolvesTheStatusInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsID := runJiraCSVStatusCategoryJob(t, d, jiraCSVStatusCategoryJobBody)

	for ident, want := range map[string]model.IssueStatus{
		"PROJ-1": model.StatusTodo,
		"PROJ-2": model.StatusInProgress,
		"PROJ-3": model.StatusDone,
		"PROJ-4": model.StatusCancelled, // recognised NAME, never overruled by the category
	} {
		var got string
		if err := d.Pool.QueryRow(ctx,
			`SELECT status FROM issues WHERE workspace_id = $1 AND identifier = $2`, wsID, ident).Scan(&got); err != nil {
			t.Fatalf("read status for %s: %v", ident, err)
		}
		if model.IssueStatus(got) != want {
			t.Errorf("%s: status = %q, want %q", ident, got, want)
		}
	}
}

// THE SECOND LOSS, IN THE COLUMN A REPORT READS. analytics' resolution-stats selects on
// `completed_at IS NOT NULL`, so a Done-category row whose status imported as backlog was withheld
// from delivered work entirely — and the abandoned row must still be withheld.
func TestJobRow_JiraCSV_StatusCategoryDoneLandsTheCompletionTime(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsID := runJiraCSVStatusCategoryJob(t, d, jiraCSVStatusCategoryJobBody)

	var completed *time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT completed_at FROM issues WHERE workspace_id = $1 AND identifier = 'PROJ-3'`, wsID).Scan(&completed); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	if completed == nil {
		t.Errorf("PROJ-3 completed_at = NULL, want the Resolved date — the completion gate reads the status the category resolves")
	}
	var abandoned *time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT completed_at FROM issues WHERE workspace_id = $1 AND identifier = 'PROJ-4'`, wsID).Scan(&abandoned); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	if abandoned != nil {
		t.Errorf("PROJ-4 completed_at = %v, want NULL — abandoned work is not delivered work", abandoned)
	}
}

// THE JOB ROW ITSELF must still report the import as clean-but-degraded rather than failed, and its
// warnings must name the path that decided each status — the structural-zero defence: a Jira export
// with no category column and one whose categories resolved every row must not produce the same
// report.
func TestJobRow_JiraCSV_StatusCategoryIsNamedInTheJobWarnings(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsID := runJiraCSVStatusCategoryJob(t, d, jiraCSVStatusCategoryJobBody)

	var status string
	var warnings []string
	if err := d.Pool.QueryRow(ctx,
		`SELECT status, warnings FROM import_jobs WHERE workspace_id = $1`, wsID).Scan(&status, &warnings); err != nil {
		t.Fatalf("read job row: %v", err)
	}
	if status != "succeeded" {
		t.Fatalf("job status = %q, want succeeded", status)
	}
	var found bool
	for _, w := range warnings {
		if w == `unrecognised status "merged to master" on 1 issue(s) — resolved via statusCategory "Done" as "done"` {
			found = true
		}
	}
	if !found {
		t.Errorf("no job warning names the statusCategory that decided the status; warnings = %#v", warnings)
	}
}
