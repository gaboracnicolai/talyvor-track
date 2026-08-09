package importer_test

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// jira_csv_resolution_job_test.go — the Resolution rule driven END TO END on real Postgres, through
// the async runner and a jira_csv job, and read back OUT OF the issues TABLE per title.
//
// ⚠ THIS IS NOT THE UNIT TEST TWICE, AND THIS PACKAGE HAS PAID FOR THAT THREE TIMES. #74 found the
// importer's UPSERT silently omitting `completed_at` so a perfectly mapped value was thrown away by
// the SQL; #78 found the SECOND copy of that seam in issue.Store.Create, which is the statement
// every CSV row takes; #76's C11 exists because a resolution dying between the mapper and Postgres
// would have reded nothing. This merge changes the STATUS the row is written with, and
// issue.Store.Create derives `completed_at` from that status ITSELF (`if issue.Status !=
// model.StatusDone { completedAt = nil }`) — so the mapper and the SQL each apply the gate, and only
// a database-level read proves the two agree.
//
// The fixture is the measured export's shape: Status is "Closed" on every resolved row, because on
// the real instance it always is — that is the whole reason the defect was invisible.
const jiraCSVResolutionRows = "Summary,Description,Status,Priority,Resolution,Resolved\n" +
	"Abandoned in postgres,d,Closed,High,Won't Fix,23/Mar/2026 4:59 PM\n" +
	"Finished in postgres,d,Closed,High,Done,06/Aug/2026 8:06 PM\n" +
	"Unreadable in postgres,d,Closed,High,Duplicate,15/Jul/2026 2:34 PM\n"

type importedRow struct {
	status      string
	hasComplete bool
}

func readIssueStatusByTitle(t *testing.T, d *testutil.DB, wsID string) map[string]importedRow {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT title, status, completed_at IS NOT NULL FROM issues WHERE workspace_id = $1`, wsID)
	if err != nil {
		t.Fatalf("read back issues: %v", err)
	}
	defer rows.Close()
	out := map[string]importedRow{}
	for rows.Next() {
		var title, status string
		var hasComplete bool
		if err := rows.Scan(&title, &status, &hasComplete); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[title] = importedRow{status, hasComplete}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

func TestJobRow_JiraCSV_AbandonedWorkLandsCancelledAndUndatedInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(jiraCSVResolutionRows))
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
	// Every row LANDS. A reclassification is not a failure and must not be counted as one — the
	// #81 lesson, one field over.
	if j.Status != importer.JobSucceeded || j.Imported != 3 || j.Failed != 0 || j.Skipped != 0 {
		t.Fatalf("job row = {status:%q imported:%d failed:%d skipped:%d}, want {succeeded 3 0 0}",
			j.Status, j.Imported, j.Failed, j.Skipped)
	}

	got := readIssueStatusByTitle(t, d, ws.ID)
	for _, tc := range []struct {
		title       string
		wantStatus  string
		wantHasDate bool
		why         string
	}{
		{"Abandoned in postgres", "cancelled", false,
			`Jira resolved it "Won't Fix"; a completion time here is counted as delivered work by resolution-stats`},
		{"Finished in postgres", "done", true, `Jira resolved it "Done" — nothing about this row may change`},
		{"Unreadable in postgres", "done", true, `Jira resolved it "Duplicate", which Track refuses to interpret — nothing may change`},
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

	// The warnings reach the JOB ROW's TEXT[], not just ImportResult — 0026's channel is the one a
	// real import is read through, and a report that stops at the struct is inert exactly there.
	var sawOverride, sawRefusal bool
	for _, w := range j.Warnings {
		if strings.Contains(w, `resolution "Won't Fix"`) && strings.Contains(w, `"cancelled"`) {
			sawOverride = true
		}
		if strings.Contains(w, `resolution "Duplicate"`) {
			sawRefusal = true
		}
	}
	if !sawOverride || !sawRefusal {
		t.Errorf("job warnings do not carry both the override and the refusal: %#v", j.Warnings)
	}
}
