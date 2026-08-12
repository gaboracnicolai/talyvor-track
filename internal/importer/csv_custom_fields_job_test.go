package importer_test

// csv_custom_fields_job_test.go — the custom-field report read back OUT OF THE JOB ROW on real
// Postgres, plus the fact about the DATABASE its sentence claims: after importing rows that carry
// custom-field values, `custom_fields` and `issue_field_values` are EMPTY.
//
// ⚠ ONE testutil.New FOR THE WHOLE FILE — internal/importer runs 55-64s on CI against
// `-timeout 120s`; see the harness note in csv_unread_refs_job_test.go.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// THREE rows carrying Severity, TWO carrying Test level, ONE carrying only the excluded Epic Link:
// the counts in the two lines must differ from each other AND from the imported-row count, so a
// report that counted rows rather than populated cells cannot pass.
//
// The Epic Link column is on the header for every row so the exclusion is exercised through the
// wire, not only in the unit test.
const jiraCSVWithCustomFields = "Issue key,Summary,Status,Priority,Custom field (Severity),Custom field (Test level),Custom field (Epic Link)\n" +
	"JRA-1,Ticket one,To Do,High,Blocker,L2,\n" +
	"JRA-2,Ticket two,To Do,High,Major,L1,\n" +
	"JRA-3,Ticket three,To Do,High,Minor,,\n" +
	"JRA-4,Ticket four,To Do,High,,,JRA-100\n"

func countCustomFieldRows(t *testing.T, d *testutil.DB, table, wsID string) int {
	t.Helper()
	var n int
	q := "SELECT count(*) FROM " + table + " WHERE workspace_id=$1"
	if table == "issue_field_values" {
		// issue_field_values has no workspace_id; it hangs off the issue.
		q = "SELECT count(*) FROM issue_field_values v JOIN issues i ON i.id=v.issue_id WHERE i.workspace_id=$1"
	}
	if err := d.Pool.QueryRow(context.Background(), q, wsID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestJobRow_JiraCSV_TheDroppedCustomFieldsReachTheJobRow(t *testing.T) {
	d := testutil.New(t)

	t.Run("one line per spelling, each with its own count", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWithCustomFields)
		if job.Status != importer.JobSucceeded || job.Imported != 4 {
			t.Fatalf("premise: job = %s imported=%d %q, want succeeded/4", job.Status, job.Imported, job.ErrorSummary)
		}

		// ⚠ THE PREMISE, ASSERTED RATHER THAN ASSUMED. If either count is ever non-zero a transport
		// has started creating the object and the warning describes a loss that no longer happens —
		// delete the entry WITH that change rather than leaving a stale warning behind.
		if n := countCustomFieldRows(t, d, "custom_fields", ws.ID); n != 0 {
			t.Fatalf("A TRANSPORT NOW CREATES CUSTOM FIELD DEFINITIONS: %d row(s). Remove the entry "+
				"in csv_custom_fields.go with that change", n)
		}
		if n := countCustomFieldRows(t, d, "issue_field_values", ws.ID); n != 0 {
			t.Fatalf("A TRANSPORT NOW CREATES CUSTOM FIELD VALUES: %d row(s)", n)
		}

		// THE TWO COUNTS DIFFER, and neither equals the 4 rows imported.
		if got := countWarningsMentioning(job, `3 issue(s) carried a "custom field (severity)"`); got != 1 {
			t.Errorf("the Severity line does not say 3 issue(s).\nwarnings: %q", job.Warnings)
		}
		if got := countWarningsMentioning(job, `2 issue(s) carried a "custom field (test level)"`); got != 1 {
			t.Errorf("the Test level line does not say 2 issue(s).\nwarnings: %q", job.Warnings)
		}
		if got := countWarningsMentioning(job, "no Track custom field is created for it and no custom field value is stored"); got != 2 {
			t.Errorf("want exactly 2 lines naming what was not created, got %d.\nwarnings: %q",
				got, job.Warnings)
		}
	})

	t.Run("the epic link is reported once, as a parent, and not as a custom field", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWithCustomFields)
		if job.Status != importer.JobSucceeded {
			t.Fatalf("premise: job = %s %q", job.Status, job.ErrorSummary)
		}
		if got := countWarningsMentioning(job, "custom field (epic link)"); got != 0 {
			t.Errorf("the epic link produced a custom-field line as well as its parent line.\n"+
				"warnings: %q", job.Warnings)
		}
		// ⚠ AND THE REPORT THAT ALREADY EXISTED MUST STILL FIRE. An exclusion that silenced the
		// parent note would pass the assertion above and would be a regression, not a fix.
		if got := countWarningsMentioning(job, `Custom field (Epic Link)`); got != 1 {
			t.Errorf("the epic link produced %d parent line(s), want 1 — the exclusion silenced the "+
				"report that already existed.\nwarnings: %q", got, job.Warnings)
		}
	})

	// ⚠ THE CELL NEVER REACHES THE JOB ROW. A custom field holds whatever the tenant's instance put
	// in it, and import_jobs.warnings is readable by every member of the workspace. This is the
	// assertion that would have caught a note keyed on the value instead of the column.
	t.Run("no cell value reaches the job row", func(t *testing.T) {
		ws := d.Workspace(t)
		team := d.Team(t, ws.ID)
		job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWithCustomFields)
		for _, cell := range []string{"Blocker", "Major", "Minor", "L1", "L2"} {
			for _, w := range job.Warnings {
				if strings.Contains(w, cell) {
					t.Errorf("a custom-field CELL reached the job row: %q in %q", cell, w)
				}
			}
		}
	})
}
