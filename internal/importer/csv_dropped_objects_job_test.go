package importer_test

// csv_dropped_objects_job_test.go — the dropped-object report read back OUT OF THE JOB ROW on real
// Postgres, plus the two facts about the DATABASE its sentence claims: after importing rows that
// carry comments and logged work, `comments` and `time_entries` are EMPTY.
//
// ⚠ ONE testutil.New FOR THE WHOLE FILE — internal/importer runs 62s on CI against `-timeout 120s`
// (measured on the merge of #118); see the harness note in csv_unread_refs_job_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// TWO rows carrying a comment, ONE carrying logged work, one carrying neither: the counts in the
// two lines must differ, so a report that counted imported rows cannot pass.
const jiraCSVWithDroppedObjects = "Issue key,Summary,Status,Priority,Comment,Time Spent,Original estimate\n" +
	"JRA-1,Ticket one,To Do,High,12/Mar/24;ada;first,3600,7200\n" +
	"JRA-2,Ticket two,To Do,High,13/Mar/24;grace;second,,7200\n" +
	"JRA-3,Ticket three,To Do,High,,,7200\n"

func countRows(t *testing.T, d *testutil.DB, table, wsID string) int {
	t.Helper()
	var n int
	q := "SELECT count(*) FROM " + table + " WHERE workspace_id=$1"
	if table == "comments" {
		// comments has no workspace_id; it hangs off the issue.
		q = "SELECT count(*) FROM comments c JOIN issues i ON i.id=c.issue_id WHERE i.workspace_id=$1"
	}
	if err := d.Pool.QueryRow(context.Background(), q, wsID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestJobRow_JiraCSV_TheDroppedObjectsReachTheJobRow(t *testing.T) {
	d := testutil.New(t)

	t.Run("one line per object column, with its own count", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWithDroppedObjects)
		if job.Status != importer.JobSucceeded || job.Imported != 3 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/3", job.Status, job.Imported, job.ErrorSummary)
		}

		// ⚠ THE PREMISE, ASSERTED RATHER THAN ASSUMED. If either count is ever non-zero a transport
		// has started creating the object and the warning describes a loss that no longer happens —
		// delete the entry WITH that change.
		if n := countRows(t, d, "comments", ws.ID); n != 0 {
			t.Fatalf("A TRANSPORT NOW CREATES COMMENTS: %d row(s). Remove the entry in "+
				"csv_dropped_objects.go with that change rather than leaving a stale warning", n)
		}
		if n := countRows(t, d, "time_entries", ws.ID); n != 0 {
			t.Fatalf("A TRANSPORT NOW CREATES TIME ENTRIES: %d row(s)", n)
		}

		if got := countWarningsMentioning(job, `2 issue(s) carried a "Comment"`); got != 1 {
			t.Errorf("the Comment line does not say 2 issue(s).\nwarnings: %q", job.Warnings)
		}
		if got := countWarningsMentioning(job, `1 issue(s) carried a "Time Spent"`); got != 1 {
			t.Errorf("the Time Spent line does not say 1 issue(s).\nwarnings: %q", job.Warnings)
		}
		if got := countWarningsMentioning(job, "no Track comment is created"); got != 1 {
			t.Errorf("the comment line does not name what was not created.\nwarnings: %q", job.Warnings)
		}
		// ⚠ AND THE COLUMN THAT HAS NO TRACK OBJECT IS SILENT ON THE SAME IMPORT, which is the half
		// that keeps this report honest: `Original estimate` is populated on ALL THREE rows and
		// must not appear anywhere in the job row.
		for _, w := range job.Warnings {
			if strings.Contains(w, "Original estimate") {
				t.Errorf("a value with no Track object was reported: %q", w)
			}
		}
		// The cell is a comment BODY and the job row is readable by every member of the workspace.
		for _, w := range job.Warnings {
			if strings.Contains(w, "ada") || strings.Contains(w, "first") || strings.Contains(w, "grace") {
				t.Errorf("a comment body reached the job row: %q", w)
			}
		}
	})

	t.Run("an import that dropped no object is silent about them", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importJiraCSVInto(t, d, ws.ID, team.ID,
			"Issue key,Summary,Status,Priority,Comment,Time Spent\nJRA-9,Ticket nine,To Do,High,,\n")
		if job.Status != importer.JobSucceeded || job.Imported != 1 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/1", job.Status, job.Imported, job.ErrorSummary)
		}
		for _, w := range job.Warnings {
			if strings.Contains(w, "is created from it") {
				t.Errorf("an import that dropped no object produced a dropped-object warning: %q", w)
			}
		}
	})
}
