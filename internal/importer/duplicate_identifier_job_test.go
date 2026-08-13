package importer_test

// duplicate_identifier_job_test.go — WHAT HAPPENS WHEN ONE EXPORT NAMES THE SAME ISSUE TWICE,
// driven END TO END on real Postgres through the shipped async runner and read back OUT OF THE
// issues TABLE.
//
// ⚠⚠ THE FINDING, MEASURED BEFORE A LINE OF THE FIX EXISTED, through this runner on real Postgres
// with REAL exports out of the cached corpus:
//
//	jira   d21ead32… (Atlassian SourceTree-Windows, 900 rows, 68 keys on two rows each)
//	       job = partial   imported=893 skipped=0 failed=7, 825 issues, 44 warnings
//	jira   6edbf7dc… (Apache commons-fileupload, 8 rows, 2 keys on two rows each, ROWS DIFFER)
//	       job = succeeded imported=8   skipped=0 failed=0, 6 issues, 32 warnings
//	linear 0888cca6… (185 rows, 43 keys on two rows each)
//	       job = succeeded imported=185 skipped=0 failed=0, 118 issues, 8 warnings
//
// Not one of those 44 + 32 + 8 warnings said the export had named an issue twice. `imported` is a
// count of ROWS WRITTEN and the workspace holds ISSUES, and the difference — 68, 2 and 67 — is
// carried by no counter, no status and no sentence.
//
// ⚠ THE ONE `partial` IS NOT THIS FINDING AND IS NAMED SO IT IS NOT MISREAD AS IT: d21ead32…'s
// `failed=7` is seven rows with no title, refused by errEmptyTitle, and it is the SAME 7 before and
// after this merge. Its 68 collapsed rows are inside `imported=893`, not inside `failed`.
//
// ⚠ AND THE SECOND ROW DOES NOT SIMPLY LOSE. UpsertByIdentifier's conflict arm CLOBBERS title,
// description and labels and PRESERVES status, priority, due_date, completed_at and created_at, so
// two rows naming one issue leave a row that is NEITHER of them: the later row's title and body on
// the earlier row's workflow state. In 6edbf7dc… that is visible in the export itself —
// `FILEUPLOAD-157` is "The ProgressListener isn't always notified about the total number of bytes"
// on one row and "The ProgressListener initialization problems." on the other.
//
// ⚠ IT REPORTS, IT DOES NOT REFUSE — the precedent is csvSource.Next's two row-width branches and
// csv_dropped_objects.go: this package's rule is that a row's landing place is not changed on a
// measurement, because WHICH of two rows naming one issue should win is a product decision (first
// wins / last wins / refuse the pair) and no session gets to pick it silently.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// Two rows, ONE key, and every column the conflict arm treats differently is different between
// them: `Title` is CLOBBERED, `Status` is PRESERVED. That is what makes the surviving row provably
// neither export row rather than merely "the second one".
const linearCSVSameKeyTwice = "ID,Team,Title,Description,Status,Priority,Labels\n" +
	"DO-201,Ops,First row title,first body,In Progress,High,alpha\n" +
	"DO-201,Ops,Second row title,second body,Todo,Low,beta\n"

// The CONTROL FIXTURE, and it is not decoration: a note that fired on every import would satisfy
// every assertion below for the worst possible reason. Same shape, two DISTINCT keys.
const linearCSVTwoDistinctKeys = "ID,Team,Title,Description,Status,Priority,Labels\n" +
	"DO-301,Ops,First row title,first body,In Progress,High,alpha\n" +
	"DO-302,Ops,Second row title,second body,Todo,Low,beta\n"

// dupJobWarningsMentioning returns the job warnings that name the identifier — the operator-facing
// surface, read back off the job row rather than off an in-memory ImportResult.
func dupJobWarningsMentioning(job *importer.Job, key string) []string {
	var out []string
	for _, w := range job.Warnings {
		if strings.Contains(w, key) {
			out = append(out, w)
		}
	}
	return out
}

// TestJobRow_OneExportNamingTheSameIssueTwiceSaysSo is the finding. The count assertion is the
// premise and the WARNING is the assertion — the rows really do collapse (that is the upsert
// working as designed and is not what this test asks to change); what must not stay true is that
// the job says nothing about it.
func TestJobRow_OneExportNamingTheSameIssueTwiceSaysSo(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVSameKeyTwice)

	// PREMISE, asserted rather than assumed: both rows were written and the workspace holds ONE
	// issue. Without this the warning assertion could pass on an import that refused a row.
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: job = %s imported=%d skipped=%d failed=%d %q, want succeeded/2 — "+
			"this test is measuring the wrong state", job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}
	if n := linearIssuesInWorkspace(t, d, ws.ID); n != 1 {
		t.Fatalf("premise: %d issues in the workspace, want 1 — the two rows did not collide", n)
	}

	got := dupJobWarningsMentioning(job, "DO-201")
	if len(got) == 0 {
		t.Errorf("an export naming DO-201 on two rows imported as %s imported=%d skipped=%d failed=%d "+
			"with %d warning(s), NONE of them naming the identifier.\n"+
			"The operator is told two rows were imported; the workspace holds one issue, carrying the "+
			"second row's title over the first row's status. Nothing in the job row can be used to "+
			"discover that.\nwarnings: %q",
			job.Status, job.Imported, job.Skipped, job.Failed, len(job.Warnings), job.Warnings)
	}
}

// TestJobRow_TwoDistinctKeysAreNotReportedAsDuplicates is the positive control ON THE REPORT: the
// same fixture shape with two different keys must produce NO duplicate-identifier line. A guard
// that fires on every import is not a guard.
func TestJobRow_TwoDistinctKeysAreNotReportedAsDuplicates(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVTwoDistinctKeys)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: job = %s imported=%d %q, want succeeded/2", job.Status, job.Imported, job.ErrorSummary)
	}
	if n := linearIssuesInWorkspace(t, d, ws.ID); n != 2 {
		t.Fatalf("premise: %d issues, want 2 — the control fixture collided", n)
	}
	for _, key := range []string{"DO-301", "DO-302"} {
		if got := dupJobWarningsMentioning(job, key); len(got) > 0 {
			t.Errorf("two DISTINCT keys produced a duplicate-identifier warning for %s: %q.\n"+
				"A report that fires on every import tells an operator nothing.", key, got)
		}
	}
}

// TestJobRow_TheSurvivingRowIsNeitherExportRow is the CONSEQUENCE, measured rather than argued. It
// is the half that makes the warning worth rendering: the row left behind carries the later row's
// title and the earlier row's status, which is a combination that appears on NO row of the export.
func TestJobRow_TheSurvivingRowIsNeitherExportRow(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVSameKeyTwice)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: job = %s imported=%d %q, want succeeded/2", job.Status, job.Imported, job.ErrorSummary)
	}

	var title, status string
	if err := d.Pool.QueryRow(ctx,
		`SELECT title, status FROM issues WHERE workspace_id=$1 AND identifier=$2`,
		ws.ID, "DO-201").Scan(&title, &status); err != nil {
		t.Fatalf("read DO-201: %v", err)
	}
	// `Title` is in the conflict SET (provider is source of truth) so the LAST row wins it.
	if title != "Second row title" {
		t.Errorf("title = %q, want the second row's %q — the conflict arm clobbers title", title, "Second row title")
	}
	// `Status` is OMITTED from the conflict SET (preserve local workflow) so the FIRST row's
	// mapped status survives. `In Progress` maps to Track's in_progress; the second row said Todo.
	if status != "in_progress" {
		t.Errorf("status = %q, want the FIRST row's %q — status is preserved by the conflict arm.\n"+
			"If this changed, the sentence the duplicate-identifier warning renders is no longer true.",
			status, "in_progress")
	}
}
