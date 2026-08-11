package importer_test

// csv_wide_row_job_test.go — the wide-row finding driven END TO END on real Postgres through the
// shipped async runner and a jira_csv job, and read back OUT OF THE issues TABLE.
//
// ⚠ WHY A JOB TEST AND NOT ONLY A SOURCE-LEVEL ONE. csv_wide_row_test.go asserts ImportResult, which
// is a value inside one function call. This asserts the two things a person actually meets: the job
// row the operator reads, and the row the database holds. They disagree in the way that makes this
// finding worth a merge — the job says
//
//	{status: "succeeded", imported: 10, skipped: 0, failed: 0}
//
// on the real export `0347210d…`, whose every issue carries a LABEL in its Description. A succeeded
// job with a full import count is the one outcome nobody re-runs and nobody investigates.
//
// ⚠ AND ImportResult.Warnings HAS TO REACH THE JOB ROW TO BE WORTH ANYTHING. It is persisted through
// migration 0026's `warnings` column; a note that renders correctly and never lands there is a
// sentence nobody will ever read. That path is what this file tests and the source-level file cannot.
//
// See csv_wide_row_test.go for the whole-population measurement (346 Jira files, 31,103 rows, 11
// wide, two unrelated instances; 0 of 3,164 Linear rows) and for the provenance limit.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// The measured shape, once. Header VERBATIM from the real export; the row supplies TWO `Labels`
// cells against a header that declares ONE, which is Jira's ordinary multi-value serialisation with
// a header that was not widened to match.
//
// ⚠ KEPT HERE RATHER THAN SHARED with the source-level fixture on purpose: the two files compile
// into DIFFERENT packages (importer vs importer_test), and a fixture reached across that seam would
// make one depend on the other's internals. Same reasoning the short-row pair records.
const jiraCSVWideRowJobExport = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Project lead,Project description,Project url,Priority,Resolution,Assignee,Reporter,Creator,Created,Updated,Last Viewed,Resolved,Component/s,Due Date,Votes,Labels,Description,Environment,Original Estimate,Remaining Estimate,Time Spent,Work Ratio\n" +
	"Enhance security protocols,EXMPL-2311,2311,Epic,Resolved,EXMPL,Example Project,Software,lead,desc,https://example.com,Medium,Fixed,alice,bob,carol,11/May/24 03:26 PM,11/May/24 07:07 AM,16/May/24 03:49 AM,20/May/24 04:53 PM,Component 1,11/May/24,27,label1, label2,the real description,specific,38h,15h,37h,99%\n"

// TestJobRow_JiraCSV_WideRowFixturePremise asserts the fixture really is wide BEFORE the job test
// trusts it. The whole finding is one surplus cell; a fixture that lost it would leave every
// assertion below passing while measuring the ordinary path.
func TestJobRow_JiraCSV_WideRowFixturePremise(t *testing.T) {
	lines := strings.Split(strings.TrimRight(jiraCSVWideRowJobExport, "\n"), "\n")
	header := strings.Count(lines[0], ",") + 1
	if header != 30 {
		t.Fatalf("header is %d columns, want the measured 30-wide shape", header)
	}
	if got := strings.Count(lines[1], ",") + 1; got != header+1 {
		t.Fatalf("row supplies %d fields against a %d-column header, want %d (wide by exactly one)",
			got, header, header+1)
	}
}

// TestJobRow_JiraCSV_AWideRowIsReportedToTheOperator is the finding, end to end.
//
// BEFORE: {status:"succeeded", imported:1, failed:0} and a warnings column that never mentioned the
// row, while `issues.description` held the string "label2".
func TestJobRow_JiraCSV_AWideRowIsReportedToTheOperator(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVWideRowJobExport)

	// The row still lands — this merge reports, it does not refuse.
	if job.Status != importer.JobSucceeded || job.Imported != 1 || job.Failed != 0 {
		t.Fatalf("job = %s imported=%d skipped=%d failed=%d %q, want succeeded imported=1 failed=0",
			job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}

	// The half that did not exist: the sentence reaches the persisted job row.
	found := ""
	for _, w := range job.Warnings {
		if strings.Contains(w, "wider than the header") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("the job row carries no row-width warning: %q.\nA note that renders correctly and "+
			"never reaches migration 0026's warnings column is a sentence nobody reads.", job.Warnings)
	}
	if !strings.Contains(found, "31 of 30 columns") {
		t.Errorf("job warning %q does not name both widths", found)
	}

	// The DATABASE half. "imported=1" is satisfiable by a row of nothing, and the whole point of the
	// warning is a value that is present and wrong — so it is read back out of `issues` by the key
	// Jira gave it. This pins the DAMAGE, not the fix: the mapping is deliberately unchanged.
	var desc string
	if err := d.Pool.QueryRow(ctx,
		`SELECT description FROM issues WHERE workspace_id=$1 AND identifier=$2`,
		ws.ID, "EXMPL-2311").Scan(&desc); err != nil {
		t.Fatalf("read back EXMPL-2311: %v", err)
	}
	if desc != "label2" {
		t.Errorf("issues.description = %q, want the shifted %q — this is what the warning is about; "+
			"if the shift is gone the warning has no subject and this file must be revisited", desc, "label2")
	}
}

// TestJobRow_JiraCSV_AnExactWidthImportCarriesNoRowShapeWarning is the must-stay-green companion at
// the job layer. Without it, a note that fired on every import would satisfy the assertions above.
func TestJobRow_JiraCSV_AnExactWidthImportCarriesNoRowShapeWarning(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	const exact = "Summary,Issue key,Status,Priority,Description\n" +
		"Enhance security protocols,EXMPL-2311,Done,Medium,the real description\n"
	job := importJiraCSVInto(t, d, ws.ID, team.ID, exact)
	if job.Status != importer.JobSucceeded || job.Imported != 1 {
		t.Fatalf("job = %s imported=%d, want succeeded imported=1", job.Status, job.Imported)
	}
	for _, w := range job.Warnings {
		if strings.Contains(w, "wider than the header") || strings.Contains(w, "narrower than the header") {
			t.Errorf("an import whose rows match its header produced a row-shape warning: %q", w)
		}
	}
}
