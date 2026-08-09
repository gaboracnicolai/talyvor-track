package importer

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// status_category_job_test.go — the finding driven END TO END on real Postgres, through the async
// runner and the jira_api source, because that is the path a real import takes: #72's fix stopped at
// ImportResult would have been inert exactly there, and so would this one.
//
// #72 pinned the counterpart of this test: four provider rows, all landing as `backlog`, with the
// job reporting itself clean. This is what changes.

// jiraAPIPage builds the one canned page the runner's jiraSource will drain.
func jiraAPIPage(issues ...string) string {
	return `{"issues":[` + strings.Join(issues, ",") + `],"isLast":true}`
}

// THE MERGE, MEASURED WHERE IT COUNTS: four statuses taken verbatim from the real instance's 46,
// none of which mapJiraStatus knows. Today all four are `backlog` rows in Postgres. After this
// merge the rows carry what Jira actually said, and the job row names the path that decided it.
func TestJobRow_JiraAPI_CategoryResolvesTheStatusInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(
		jiraIssueWithCategoryJSON("PROJ-1", "Gathering", "Gathering Interest", "new", "To Do"),
		jiraIssueWithCategoryJSON("PROJ-2", "QA it", "Ready for QA", "indeterminate", "In Progress"),
		jiraIssueWithCategoryJSON("PROJ-3", "Shipped", "Implemented", "done", "Done"),
		jiraIssueWithCategoryJSON("PROJ-4", "Also shipped", "Published", "done", "Done"),
	)}, jiraAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "jira", "me@corp.com:api-token", "PROJ", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "jira_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	// Read the STATUS COLUMN back, per identifier. Asserting a count would let one row's status
	// stand in for another's; this says which issue got which answer.
	for ident, want := range map[string]model.IssueStatus{
		"PROJ-1": model.StatusTodo,
		"PROJ-2": model.StatusInProgress,
		"PROJ-3": model.StatusDone,
		"PROJ-4": model.StatusDone,
	} {
		var got model.IssueStatus
		if err := d.Pool.QueryRow(ctx,
			`SELECT status FROM issues WHERE workspace_id=$1 AND identifier=$2`, ws.ID, ident).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", ident, err)
		}
		if got != want {
			t.Errorf("%s landed as %q, want %q — before this merge every one of these was backlog", ident, got, want)
		}
	}
	// And NOTHING may still be backlog: a mapper that resolved three of four would pass every
	// assertion above if the fourth were also asserted as backlog by mistake.
	var backlog int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE workspace_id=$1 AND status=$2`, ws.ID, model.StatusBacklog).Scan(&backlog); err != nil {
		t.Fatal(err)
	}
	if backlog != 0 {
		t.Errorf("%d row(s) still landed as backlog; Jira classified all four", backlog)
	}

	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobSucceeded || j.Imported != 4 || j.Failed != 0 {
		t.Fatalf("job = {status:%s imported:%d failed:%d}, want {succeeded 4 0}", j.Status, j.Imported, j.Failed)
	}
	joined := strings.Join(j.Warnings, "\n")
	if !strings.Contains(joined, "resolved via statusCategory") {
		t.Fatalf("the job row must report that the category decided these rows; warnings =\n%s", joined)
	}
	// The count collapses per (value, path): PROJ-3 and PROJ-4 are two DIFFERENT names, so three
	// distinct lines, not one and not four.
	if len(j.Warnings) != 4 {
		t.Errorf("warnings = %d lines, want 4 (one per distinct provider status):\n%s", len(j.Warnings), joined)
	}
}

// THE STRUCTURAL-ZERO CASE, end to end: a Jira that sends no statusCategory at all. The rows are
// still backlog — unchanged, deliberately — and the JOB ROW says the field never arrived. This is
// the line an operator reads to learn that the category code did not run on their tenant, and it is
// the whole reason the read is not silent.
func TestJobRow_JiraAPI_NoCategoryIsReportedNotHidden(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(
		jiraIssueWithCategoryJSON("PROJ-1", "a", "Deployed", "", ""),
		jiraIssueWithCategoryJSON("PROJ-2", "b", "Deployed", "", ""),
	)}, jiraAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "jira", "me@corp.com:api-token", "PROJ", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "jira_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	var backlog int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE workspace_id=$1 AND status=$2`, ws.ID, model.StatusBacklog).Scan(&backlog); err != nil {
		t.Fatal(err)
	}
	if backlog != 2 {
		t.Fatalf("with no category the fallback is unchanged: backlog rows = %d, want 2", backlog)
	}
	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(j.Warnings, "\n")
	if !strings.Contains(joined, "no statusCategory present") || !strings.Contains(joined, "2 issue(s)") {
		t.Fatalf("the job row must say the category never arrived, with its count; warnings =\n%s", joined)
	}
	if strings.Contains(joined, "resolved via") {
		t.Fatalf("nothing was resolved here; the report must not imply the read worked:\n%s", joined)
	}
}
