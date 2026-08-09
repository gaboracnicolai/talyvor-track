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

// linear_state_type_job_test.go — the finding driven END TO END on real Postgres, through the async
// runner and the linear_api source, because that is the path a real import takes.
//
// ⚠ THIS FILE IS NOT THE UNIT TEST TWICE, and #74's C9 measured why it has to exist: that merge's
// mapper was correct and the importer's upsert did not NAME `completed_at`, so a perfectly mapped
// value was discarded by the SQL and every source-level assertion stayed green. The Linear path had
// a second version of the same hole — TestRunner_LinearAPI_EndToEnd drives this exact route and
// asserts only the JOB ROW's {status, imported}. It never reads an issue's status column back, so a
// resolution that died between the mapper and Postgres would not have reded anything here either.

// linearAPIPage builds the one canned page the runner's linearSource will drain.
func linearAPIPage(nodes ...string) string { return linPage(false, "", nodes...) }

// THE MERGE, MEASURED WHERE IT COUNTS: four state names Linear's own default workflow does not use,
// so mapLinearStatus knows none of them. Today all four are `backlog` rows in Postgres and the job
// reports itself clean — #72's "data loss reported as success", on the provider this item has left
// untouched through four merges.
func TestJobRow_LinearAPI_StateTypeResolvesTheStatusInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{linearAPIPage(
		linNodeTyped("ENG-1", "Icebox", "backlog", 1),
		linNodeTyped("ENG-2", "Ready", "unstarted", 2),
		linNodeTyped("ENG-3", "Shipped", "completed", 3),
		linNodeTyped("ENG-4", "Won't Fix", "canceled", 4),
	)}, linearAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "linear", "api-token", "LINEAR-TEAM-KEY", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "linear_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	// Read the STATUS COLUMN back, PER IDENTIFIER. A count would let one row's status stand in for
	// another's; this says which issue got which answer.
	for ident, want := range map[string]model.IssueStatus{
		"ENG-1": model.StatusBacklog,
		"ENG-2": model.StatusTodo,
		"ENG-3": model.StatusDone,
		"ENG-4": model.StatusCancelled,
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
	// ENG-1 is deliberately a `backlog` ANSWER, so "everything is still backlog" cannot pass the
	// loop above by accident: the three that MOVED are pinned to three distinct non-backlog values.
	var backlog int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE workspace_id=$1 AND status=$2`, ws.ID, model.StatusBacklog).Scan(&backlog); err != nil {
		t.Fatal(err)
	}
	if backlog != 1 {
		t.Errorf("%d row(s) landed as backlog, want exactly 1 (ENG-1, whose type IS backlog)", backlog)
	}

	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobSucceeded || j.Imported != 4 || j.Failed != 0 {
		t.Fatalf("job = {status:%s imported:%d failed:%d}, want {succeeded 4 0}", j.Status, j.Imported, j.Failed)
	}
	joined := strings.Join(j.Warnings, "\n")
	if !strings.Contains(joined, "resolved via state.type") {
		t.Fatalf("the job row must report that the state type decided these rows; warnings =\n%s", joined)
	}
	// A resolution is still a degradation: four distinct provider names ⇒ four lines, not one.
	if len(j.Warnings) != 4 {
		t.Errorf("warnings = %d lines, want 4 (one per distinct provider state name):\n%s", len(j.Warnings), joined)
	}
}

// THE STRUCTURAL-ZERO CASE, end to end: a Linear response with no `type` at all — which is what a
// tenant sees if the query change is ever reverted, or if some future proxy strips the field. The
// rows are still backlog, unchanged and deliberately, and the JOB ROW says the field never arrived.
// That line is the only thing that distinguishes "this code did not run" from "your states all
// resolved", and it is the entire reason the read is not silent.
func TestJobRow_LinearAPI_NoStateTypeIsReportedNotHidden(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{linearAPIPage(
		linNodeTyped("ENG-7", "Shipped", "", 1),
	)}, linearAPIPage()))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "linear", "api-token", "LINEAR-TEAM-KEY", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "linear_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).
		WithProviderConfig(istore).WithHTTPClient(srv.Client())
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	var got model.IssueStatus
	if err := d.Pool.QueryRow(ctx,
		`SELECT status FROM issues WHERE workspace_id=$1 AND identifier=$2`, ws.ID, "ENG-7").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != model.StatusBacklog {
		t.Errorf("with no type the fallback is UNCHANGED: ENG-7 landed as %q, want backlog", got)
	}

	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(j.Warnings, "\n")
	if !strings.Contains(joined, "no state.type present") {
		t.Fatalf("a Linear import that never saw a state.type must say so in the JOB ROW; warnings =\n%s", joined)
	}
	if strings.Contains(joined, "resolved via state.type") {
		t.Fatalf("nothing resolved this row; warnings =\n%s", joined)
	}
}
