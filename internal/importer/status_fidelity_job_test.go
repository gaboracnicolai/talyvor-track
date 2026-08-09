package importer_test

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// The ASYNC path is the one built for a real import (the inline route dies on the 30s timeout), so
// the warnings have to reach the JOB ROW or the fix is inert exactly where it matters. Driven on
// real Postgres and read back through JobStore.Get — the same call the status endpoint makes.

const shippedWorkCSV = "Summary,Description,Status,Priority,Labels\n" +
	"Shipped last week,d,Deployed,High,bug\n" +
	"Also shipped,d,Deployed,High,bug\n" +
	"Escalated one,d,Waiting for customer,P1,bug\n" +
	"A real backlog item,d,Backlog,Low,bug\n"

func TestJobRow_CarriesTheWarnings(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(shippedWorkCSV))
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
	// The import genuinely succeeded — every row landed. A degraded field is not a failed row,
	// and turning this job 'partial' would erase that distinction.
	if j.Status != importer.JobSucceeded || j.Imported != 4 || j.Failed != 0 {
		t.Fatalf("job = {status:%s imported:%d failed:%d}, want {succeeded 4 0}", j.Status, j.Imported, j.Failed)
	}
	joined := strings.Join(j.Warnings, "\n")
	for _, want := range []string{`"Deployed"`, `2 issue`, `"Waiting for customer"`, `"P1"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("the job row must carry %s; warnings =\n%s", want, joined)
		}
	}
	// The read is real, not a default: assert against the column itself too.
	var stored []string
	if err := d.Pool.QueryRow(ctx, `SELECT warnings FROM import_jobs WHERE id=$1`, jobID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != len(j.Warnings) || len(stored) == 0 {
		t.Fatalf("column holds %d warnings, Get returned %d — and neither may be zero here", len(stored), len(j.Warnings))
	}
}

// The other direction, so the column cannot pass by always being full: a fully recognised import
// stores an EMPTY array, never NULL (the column is NOT NULL and Get must not invent a nil).
func TestJobRow_CleanImportStoresEmptyWarnings(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	// `Created` is present because a real export always carries it; without it the import is
	// legitimately warned about (jira_csv_created.go) and this assertion would have had to be
	// weakened rather than the fixture made realistic.
	clean := "Summary,Description,Status,Priority,Labels,Created\n" +
		"One,d,To Do,Highest,bug,23/Jul/2026 7:36 PM\nTwo,d,Resolved,Low,bug,23/Jul/2026 7:36 PM\n"
	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(clean))
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
	if len(j.Warnings) != 0 {
		t.Fatalf("a clean import must store no warnings; got %v", j.Warnings)
	}
	if j.Warnings == nil {
		t.Error("Warnings must scan as an empty slice, not nil — the JSON contract is [] not null")
	}
	// And prove the rows really did map, so this test cannot pass because nothing imported.
	var done, todo int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE status=$2), count(*) FILTER (WHERE status=$3)
		 FROM issues WHERE workspace_id=$1`,
		ws.ID, model.StatusDone, model.StatusTodo).Scan(&done, &todo); err != nil {
		t.Fatal(err)
	}
	if done != 1 || todo != 1 {
		t.Fatalf("issues by status = done:%d todo:%d, want 1/1 — a green 'no warnings' with nothing imported proves nothing", done, todo)
	}
}

// THE FINDING ITSELF, end to end on real Postgres: two issues that are FINISHED in the provider
// land as `backlog`. That fallback is unchanged by this merge — what changed is that the job no
// longer reports the run as clean. This test pins BOTH halves so a future merge that starts
// mapping "Deployed" has to come here and say so.
func TestJobRow_FinishedIssuesStillLandAsBacklogButAreReported(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(shippedWorkCSV))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var backlog int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE workspace_id=$1 AND status=$2`, ws.ID, model.StatusBacklog).Scan(&backlog); err != nil {
		t.Fatal(err)
	}
	if backlog != 4 {
		t.Fatalf("backlog rows = %d, want 4 — the fallback is deliberately unchanged by this merge", backlog)
	}
	j, err := js.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Warnings) == 0 {
		t.Fatal("three of those four rows were rewritten; the job row must say so")
	}
}
