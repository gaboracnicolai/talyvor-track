package importer_test

// linear_csv_short_row_job_test.go — the short-row finding driven END TO END on real Postgres
// through the shipped async runner and a linear_csv job, and read back OUT OF THE issues TABLE.
//
// ⚠ WHY A JOB TEST AND NOT ONLY A SOURCE-LEVEL ONE. The source-level test in
// linear_csv_short_row_test.go asserts ImportResult; this one asserts what the OPERATOR is told and
// what the DATABASE holds, which are different objects and disagree in a way that matters here:
// ImportResult.Skipped is written to the job row's `failed` column (see the crossover named in
// ImportResult's own doc comment). So before this merge the operator running
// pathliving/nordic's export saw:
//
//	{status: "failed", imported: 0, skipped: 0, failed: 28}
//
// on a file in which all 28 rows carried a well-formed ID, Title, Status, Priority and Created.
// A "failed" job with a real error count is the one outcome nobody re-runs and nobody investigates
// as a Track bug — it reads as a broken export.
//
// See linear_csv_short_row_test.go for the whole-population measurement (45 files, 3,099 rows, 73
// short, two owners, 100% of the data in both affected files) and for the alignment controls.

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// The measured shape, once. Kept in this file rather than shared with the source-level test on
// purpose: these two files are compiled into DIFFERENT packages (importer vs importer_test), and a
// fixture reached across that seam would make one of them depend on the other's internals.
const linearCSVShortRowJobExport = "ID,Team,Title,Description,Status,Estimate,Priority,Project ID,Project,Creator,Assignee,Labels,Cycle Number,Cycle Name,Cycle Start,Cycle End,Created,Updated,Started,Triaged,Completed,Canceled,Archived,Due Date,Parent issue,Initiatives,Project Milestone ID,Project Milestone,SLA Status,Roadmaps\n" +
	"IN-10,Nordic-app,License,Add licence to the nordic app,Done,,High,,,pathliving@gmail.com,pathliving@gmail.com,Improvement,,,,,2024-05-14T08:53:33Z,2024-05-23T16:06:19Z,,,2024-05-23T16:06:19Z,,,,,,,,\n" +
	"IN-11,Nordic-app,Design tokens,Body two,Todo,,Medium,,,pathliving@gmail.com,pathliving@gmail.com,Improvement,,,,,2024-05-14T09:01:00Z,2024-05-23T16:10:00Z,,,,,,,,,,,\n"

// TestJobRow_LinearCSV_ShortRowFixturePremise asserts the fixture really is narrow BEFORE the job
// test trusts it. Same reasoning as its twin in the other package: the entire finding is one
// trailing comma, and a fixture that silently gained one would turn every assertion below into a
// test of the ordinary path wearing this file's name.
func TestJobRow_LinearCSV_ShortRowFixturePremise(t *testing.T) {
	lines := strings.Split(strings.TrimRight(linearCSVShortRowJobExport, "\n"), "\n")
	header := strings.Count(lines[0], ",") + 1
	if header != 30 {
		t.Fatalf("header is %d columns, want the measured 30-wide shape", header)
	}
	for i, l := range lines[1:] {
		if got := strings.Count(l, ",") + 1; got != header-1 {
			t.Errorf("row %d supplies %d fields against a %d-column header, want %d (short by exactly one)",
				i+1, got, header, header-1)
		}
	}
}

// TestJobRow_LinearCSV_AnExportThatOmitsItsTrailingFieldImports is the finding.
//
// BEFORE: {status:"failed", imported:0, skipped:0, failed:2}, zero rows in `issues`.
func TestJobRow_LinearCSV_AnExportThatOmitsItsTrailingFieldImports(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVShortRowJobExport)
	if job.Status != importer.JobSucceeded || job.Imported != 2 || job.Failed != 0 {
		t.Errorf("job = %s imported=%d skipped=%d failed=%d %q, want succeeded imported=2 failed=0.\n"+
			"MEASURED whole-population: 73 of 3,099 rows across 45 real Linear exports are narrower "+
			"than their header, and in the two files that carry them they are 100%% of the data — so "+
			"those exports import NOTHING and are reported to the operator as a failed job.",
			job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}

	// The DATABASE half. "imported=2" is satisfiable by two rows of nothing, so the columns are
	// read back out of `issues` by the key Linear gave them.
	for _, want := range []struct{ key, title, status string }{
		{"IN-10", "License", "done"},
		{"IN-11", "Design tokens", "todo"},
	} {
		var title, status string
		err := d.Pool.QueryRow(ctx,
			`SELECT title, status FROM issues WHERE workspace_id=$1 AND identifier=$2`,
			ws.ID, want.key).Scan(&title, &status)
		if err != nil {
			t.Errorf("no issue addressable as %q after a linear_csv import: %v", want.key, err)
			continue
		}
		if title != want.title || status != want.status {
			t.Errorf("%s: title=%q status=%q, want %q/%q", want.key, title, status, want.title, want.status)
		}
	}
}

// TestJobRow_LinearCSV_TheShortRowKeepsTheDatesLinearSupplied is the half that would still be
// broken if the row landed with its tail read as empty from the WRONG index. `Created` is column
// 16 of 30 and `Completed` is column 20 — both well inside a row that stops at 29, and both are
// what four earlier merges on this item were about. If the truncation shifted the row, these would
// be the first columns to go wrong, and a title-only assertion could not see it.
func TestJobRow_LinearCSV_TheShortRowKeepsTheDatesLinearSupplied(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVShortRowJobExport)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: job = %s imported=%d, want succeeded/2 — nothing to assert dates on",
			job.Status, job.Imported)
	}

	var createdYear, completedYear *int
	err := d.Pool.QueryRow(ctx,
		`SELECT EXTRACT(YEAR FROM created_at)::int,
		        EXTRACT(YEAR FROM completed_at)::int
		   FROM issues WHERE workspace_id=$1 AND identifier=$2`, ws.ID, "IN-10").
		Scan(&createdYear, &completedYear)
	if err != nil {
		t.Fatalf("read IN-10: %v", err)
	}
	// 2024, not the import year. issues.created_at is DEFAULT NOW(), so a dropped Created is a
	// plausible timestamp rather than a null — #83's shape, and the reason this asserts the value
	// rather than non-nullness.
	if createdYear == nil || *createdYear != 2024 {
		t.Errorf("created_at year = %v, want 2024 — the `Created` column was supplied on this row", deref(createdYear))
	}
	if completedYear == nil || *completedYear != 2024 {
		t.Errorf("completed_at year = %v, want 2024 — the `Completed` column was supplied on this row", deref(completedYear))
	}
}

// TestJobRow_LinearCSV_TheShortRowIsReportedInTheJobWarnings is the reporting half, asserted where
// the operator actually reads it: the job row's warnings, which migration 0026 added a column for.
// An import that quietly read a truncated column as empty would be the structural-zero class this
// package reports everywhere else.
func TestJobRow_LinearCSV_TheShortRowIsReportedInTheJobWarnings(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVShortRowJobExport)
	joined := strings.Join(job.Warnings, "\n")
	if !strings.Contains(joined, "narrower than the header") {
		t.Errorf("job warnings = %v, want one naming a narrow row.\n"+
			"The job row is where an operator sees this; a warning that reaches ImportResult and not "+
			"the job row is a warning nobody reads.", job.Warnings)
	}
	if strings.Count(joined, "narrower than the header") != 1 {
		t.Errorf("job warnings = %v, want exactly ONE narrow-row line for two rows — #80's bound",
			job.Warnings)
	}
}

// deref renders a nullable year for a failure message. A bare %v on a *int prints the ADDRESS, and
// an address in a failure message is one more thing between the reader and the number — which is
// exactly what happened on the first run of this file ("created_at year = 0x693625e36f20").
func deref(p *int) any {
	if p == nil {
		return "NULL"
	}
	return *p
}
