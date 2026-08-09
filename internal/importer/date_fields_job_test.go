package importer

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// date_fields_job_test.go — the same finding driven END TO END on real Postgres, through the async
// runner and the jira_api source, and read back OUT OF THE COLUMNS.
//
// ⚠ THIS IS THE TEST THAT MATTERS, AND IT IS NOT THE UNIT TEST TWICE. Mapping a field onto
// model.Issue proves nothing about whether it is WRITTEN: measured before this merge, the importer's
// upsert INSERT names `due_date` but does NOT name `completed_at` at all, so a perfectly mapped
// CompletedAt is discarded by the SQL while every source-level assertion still passes. A fix that
// stopped at the mapper would have been inert exactly here — #72's lesson, one layer down.

// issueDates is what this test reads back: the two columns plus the status that governs one of them.
type issueDates struct {
	status      model.IssueStatus
	dueDate     *time.Time
	completedAt *time.Time
}

func readIssueDates(t *testing.T, d *testutil.DB, wsID string) map[string]issueDates {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT identifier, status, due_date, completed_at FROM issues WHERE workspace_id = $1`, wsID)
	if err != nil {
		t.Fatalf("read back issues: %v", err)
	}
	defer rows.Close()
	out := map[string]issueDates{}
	for rows.Next() {
		var id, status string
		var due, completed *time.Time
		if err := rows.Scan(&id, &status, &due, &completed); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = issueDates{status: model.IssueStatus(status), dueDate: due, completedAt: completed}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

// Four rows, each a distinct case, all shaped like the measured response:
//
//	PROJ-1 done      + both dates       → both columns populated
//	PROJ-2 cancelled + resolutiondate   → due only; the completion time refused AND reported
//	PROJ-3 todo      + duedate only     → due only, nothing reported
//	PROJ-4 todo      + neither          → both NULL, nothing reported
func TestJobRow_JiraAPI_DatesLandInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{jiraAPIPage(
		jiraIssueWithDatesJSON("PROJ-1", "Shipped", "Done", realJiraDueDate, realJiraResolutionDate),
		jiraIssueWithDatesJSON("PROJ-2", "Abandoned", "Won't Do", realJiraDueDate, realJiraResolutionDate),
		jiraIssueWithDatesJSON("PROJ-3", "Planned", "To Do", realJiraDueDate, ""),
		jiraIssueWithDatesJSON("PROJ-4", "Bare", "To Do", "", ""),
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

	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobSucceeded || j.Imported != 4 || j.Failed != 0 {
		t.Fatalf("job = {status:%s imported:%d failed:%d}, want {succeeded 4 0}", j.Status, j.Imported, j.Failed)
	}

	got := readIssueDates(t, d, ws.ID)
	if len(got) != 4 {
		t.Fatalf("rows in Postgres = %d, want 4", len(got))
	}

	const wantDue = "2027-12-31T00:00:00Z"
	for _, id := range []string{"PROJ-1", "PROJ-2", "PROJ-3"} {
		r := got[id]
		if r.dueDate == nil {
			t.Errorf("%s: due_date IS NULL in Postgres; the provider sent %q", id, realJiraDueDate)
			continue
		}
		if s := r.dueDate.UTC().Format(time.RFC3339); s != wantDue {
			t.Errorf("%s: due_date = %s, want %s", id, s, wantDue)
		}
	}
	if got["PROJ-4"].dueDate != nil {
		t.Errorf("PROJ-4: due_date = %v, want NULL — the provider sent none", got["PROJ-4"].dueDate)
	}

	// The done issue carries its completion time.
	if r := got["PROJ-1"]; r.completedAt == nil {
		t.Errorf("PROJ-1: completed_at IS NULL in Postgres on a DONE issue; the provider sent %q", realJiraResolutionDate)
	} else if s, want := r.completedAt.UTC().Format(time.RFC3339), "2026-08-06T20:06:39Z"; s != want {
		t.Errorf("PROJ-1: completed_at = %s, want %s", s, want)
	}

	// The cancelled issue does not — Track stamps a completion time only on "done", and analytics
	// counts every non-null completed_at as delivered work with NO status predicate.
	if r := got["PROJ-2"]; r.completedAt != nil {
		t.Errorf("PROJ-2: completed_at = %v on a %s issue", r.completedAt, r.status)
	}
	for _, id := range []string{"PROJ-3", "PROJ-4"} {
		if got[id].completedAt != nil {
			t.Errorf("%s: completed_at = %v, want NULL", id, got[id].completedAt)
		}
	}

	// AND THE REFUSAL IS ON THE JOB ROW. A deliberate drop nobody is told about is #71's "data loss
	// reported as success" — the warnings channel 0026 added exists for exactly this.
	joined := strings.Join(j.Warnings, "\n")
	if !strings.Contains(joined, realJiraResolutionDate) || !strings.Contains(joined, "cancelled") {
		t.Errorf("the refused completion time is not on the job row; warnings =\n%s", joined)
	}
	// Exactly one line: one distinct (field, value, reason), whatever the row count — #72's shape.
	if len(j.Warnings) != 1 {
		t.Errorf("warnings = %d lines, want 1 (one refused resolution date):\n%s", len(j.Warnings), joined)
	}
}

// THE LIMIT THIS MERGE CHOSE, PINNED SO IT CANNOT QUIETLY STOP BEING TRUE.
//
// Both columns land on INSERT and are OMITTED from the upsert's DO UPDATE, so a RE-import does not
// move them. That is deliberate on both halves and for different reasons:
//
//   - completed_at cannot be split from status. status is preserved on re-import so local workflow
//     wins; clobbering completed_at alone could leave a locally-done issue reading status "done"
//     with no completion time — the exact invariant issue.Store.Update maintains, broken from the
//     other side. This half is an INVARIANT, and the second assertion below is what defends it.
//   - due_date is preserved because whether a provider's plan should overwrite a local edit is a
//     DECISION (title, description and labels are deliberately clobbered; status and priority
//     deliberately are not) and it is not this merge's to make silently. This half is a STATED
//     LIMIT — a re-imported issue keeps the due date it first arrived with. It is on the queue.
//
// ⚠ THIS TEST PASSED THE FIRST TIME IT RAN, so it was positive-controlled rather than trusted:
// adding either column to the DO UPDATE set turns it red. See scripts/w34-date-controls.py.
func TestJobRow_JiraAPI_ReimportDoesNotMoveTheDateColumns(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// Two servers: the same issue key, a different due date, and a status the provider now calls
	// done. cannedPages serves page-then-empty, so each import drains exactly one page.
	first := httptest.NewServer(cannedPages([]string{jiraAPIPage(
		jiraIssueWithDatesJSON("PROJ-1", "Planned", "To Do", "2027-12-31", ""),
	)}, jiraAPIPage()))
	defer first.Close()
	second := httptest.NewServer(cannedPages([]string{jiraAPIPage(
		jiraIssueWithDatesJSON("PROJ-1", "Planned", "Done", "2028-06-30", realJiraResolutionDate),
	)}, jiraAPIPage()))
	defer second.Close()

	istore := testIntegrationStore(t, d)
	runImport := func(url string) {
		t.Helper()
		if _, err := istore.Upsert(ctx, ws.ID, "jira", "me@corp.com:api-token", "PROJ", url); err != nil {
			t.Fatal(err)
		}
		insertAPIJob(t, d, ws.ID, team.ID, "jira_api")
		client := first.Client()
		if url == second.URL {
			client = second.Client()
		}
		runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
			WithProviderConfig(istore).WithHTTPClient(client)
		if did, err := runner.RunOnce(ctx); err != nil || !did {
			t.Fatalf("RunOnce did=%v err=%v", did, err)
		}
	}

	runImport(first.URL)
	if got := readIssueDates(t, d, ws.ID)["PROJ-1"]; got.dueDate == nil ||
		got.dueDate.UTC().Format(time.RFC3339) != "2027-12-31T00:00:00Z" {
		t.Fatalf("first import: due_date = %v, want 2027-12-31 — the rest of this test needs it", got.dueDate)
	}

	// A user marks it done IN TRACK. Update stamps completed_at — Track's own invariant, and the
	// state the re-import must not damage.
	var id string
	if err := d.Pool.QueryRow(ctx,
		`SELECT id FROM issues WHERE workspace_id=$1 AND identifier=$2`, ws.ID, "PROJ-1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := issue.NewStore(d.Pool).Update(ctx, id, ws.ID,
		map[string]any{"status": string(model.StatusDone)}); err != nil {
		t.Fatal(err)
	}
	locally := readIssueDates(t, d, ws.ID)["PROJ-1"]
	if locally.completedAt == nil {
		t.Fatalf("Track did not stamp completed_at on a transition to done — precondition failed")
	}

	runImport(second.URL)
	after := readIssueDates(t, d, ws.ID)["PROJ-1"]

	// THE STATED LIMIT: the provider's new due date does not land on a re-import.
	if after.dueDate == nil || after.dueDate.UTC().Format(time.RFC3339) != "2027-12-31T00:00:00Z" {
		t.Errorf("due_date = %v after re-import, want the original 2027-12-31 — if this changed, the "+
			"decision in the upsert's OMITTED list was made without saying so", after.dueDate)
	}

	// THE INVARIANT: the locally-stamped completion time survives, and still matches the status.
	if after.completedAt == nil {
		t.Errorf("completed_at was wiped by a re-import on a locally-done issue — status is %q with no "+
			"completion time, a state Update never produces", after.status)
	} else if !after.completedAt.Equal(*locally.completedAt) {
		t.Errorf("completed_at moved from %v to %v on re-import", locally.completedAt, after.completedAt)
	}
}
