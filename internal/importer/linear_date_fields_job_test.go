package importer

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// linear_date_fields_job_test.go — the two columns read back out of Postgres, through the async
// runner and the linear_api source.
//
// ⚠ THIS IS NOT THE UNIT TEST TWICE, AND #74 PAID FOR THE LESSON ON THE JIRA SIDE. That merge's
// mapper was correct and the importer's upsert INSERT did not NAME `completed_at` at all, so a
// perfectly mapped value was discarded by the SQL while every source-level assertion stayed green.
// The SQL is shared, so the Linear path INHERITS #74's fix — but "inherits" is an assumption, and
// this item's whole history is assumptions about the layer below turning out to be structural zeros.
// So it is read back per identifier from the real columns instead.

func TestJobRow_LinearAPI_DateFieldsLandInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{linearAPIPage(
		// ENG-1: done, both dates — both must land.
		linNodeDated("ENG-1", "Done", "completed", "2026-09-01", "2026-08-01T10:00:00.000Z", 1),
		// ENG-2: in flight, carries a completedAt anyway — refused AND reported.
		linNodeDated("ENG-2", "Needs Review", "started", "2026-10-15", "2026-08-02T10:00:00.000Z", 2),
		// ENG-3: a due date in a shape no pinned layout accepts — refused AND reported, never nil'd
		// silently. This is the case the evidence cannot cover, so it is the one that must be loud.
		linNodeDated("ENG-3", "Done", "completed", "next tuesday", "", 3),
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

	read := func(ident string) (*time.Time, *time.Time) {
		t.Helper()
		var due, completed *time.Time
		if err := d.Pool.QueryRow(ctx,
			`SELECT due_date, completed_at FROM issues WHERE workspace_id=$1 AND identifier=$2`,
			ws.ID, ident).Scan(&due, &completed); err != nil {
			t.Fatalf("read %s: %v", ident, err)
		}
		return due, completed
	}

	due, completed := read("ENG-1")
	if due == nil {
		t.Error("ENG-1: due_date is NULL in Postgres — the provider sent one and the column exists")
	} else if got := due.UTC().Format("2006-01-02"); got != "2026-09-01" {
		t.Errorf("ENG-1: due_date = %s, want 2026-09-01", got)
	}
	if completed == nil {
		t.Error("ENG-1: completed_at is NULL — this is exactly the column #74 found the SQL omitting")
	} else if want := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC); !completed.Equal(want) {
		t.Errorf("ENG-1: completed_at = %v, want %v", completed, want)
	}

	// ENG-2 pins the two halves APART: the due date lands while the completion time is refused. A
	// test asserting only "some date landed" would pass with the refusal broken.
	due, completed = read("ENG-2")
	if due == nil {
		t.Error("ENG-2: due_date must land even though the completion time is refused")
	}
	if completed != nil {
		t.Errorf("ENG-2: completed_at = %v on an issue that imported as in_progress; Track stamps it "+
			"only on done, and analytics counts any non-null as delivered work", completed)
	}

	due, completed = read("ENG-3")
	if due != nil {
		t.Errorf("ENG-3: an unparseable date must never be invented: %v", due)
	}
	if completed != nil {
		t.Errorf("ENG-3: no completion time arrived: %v", completed)
	}

	j, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobSucceeded || j.Imported != 3 {
		t.Fatalf("job = {status:%s imported:%d}, want {succeeded 3}", j.Status, j.Imported)
	}
	joined := strings.Join(j.Warnings, "\n")
	// BOTH refusals must reach the JOB ROW — that is the channel a real import is read through, and
	// #72's fix stopping at ImportResult would have been inert exactly here.
	for _, want := range []string{
		"not a date shape this importer recognises", // ENG-3's due date
		"completion time only",                      // ENG-2's refused completedAt
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the job row must report %q; warnings =\n%s", want, joined)
		}
	}
	// ⚠ AND ENG-1 MUST CONTRIBUTE NO WARNING. If a clean row warned, the channel would fill with
	// noise on every import and the two lines above would stop being read.
	if strings.Contains(joined, "2026-09-01") {
		t.Errorf("ENG-1 landed cleanly and must not be reported; warnings =\n%s", joined)
	}
}
