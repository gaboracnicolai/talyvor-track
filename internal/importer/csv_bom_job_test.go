package importer_test

// csv_bom_job_test.go — the SAME three bytes driven END TO END on real Postgres, through the
// shipped async runner and a jira_csv job, and read back OUT OF THE issues TABLE.
//
// ⚠ THIS IS NOT THE UNIT TEST TWICE, AND THERE ARE TWO REASONS.
//
//  1. WHAT THE OPERATOR IS TOLD IS ON THE JOB ROW, NOT IN ImportResult. A source-level assertion
//     can see imported=0; only this one can see that the job lands in `failed` with a real error
//     count — {status:"failed", imported:0, skipped:0, failed:N} — which is the single outcome
//     nobody re-runs and nobody investigates as a Track bug. #102 measured the identical shape on
//     the Linear half and that is what made it a finding rather than a curiosity.
//
//  2. fakeIssueStore IMPLEMENTS Create AND NOT UpsertByIdentifier, so imp.upserter is nil there and
//     EVERY row takes the Create branch whatever its Identifier says. The routing claim — that a
//     BOM'd Linear export would land under a Track-derived <team>-<n> and duplicate on re-import —
//     is structurally invisible to that fake. issue.Store implements both, so it is visible here.
//
// MEASURED ON THESE FILES BEFORE THE FIX, through the runner on real Postgres: a two-row Jira
// export differing from an importing one by exactly three leading bytes produced
// {status:"failed", imported:0, skipped:0, failed:2} and ZERO rows in the issues table.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/testutil"
)

// The same verbatim bytes csv_bom_test.go uses, restated here because this file is in the _test
// package and cannot see the unexported constants. The BOM is written as an escape for the same
// reason it is there: a byte nobody can see in a diff is a byte that silently goes missing.
const jobBOM = "\uFEFF"

const jobJiraCSVBOMdExport = jobBOM +
	"Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Priority,Reporter,Created,Updated\n" +
	"Agregar Producto,QUAS-1,10000,Task,To Do,QUAS,Quantum-Stock,software,Medium,Romario Isaias Abreu Santos,6/21/2025 16:14,6/21/2025 17:13\n" +
	"Desarrollo del Backend,QUAS-3,10002,Epic,To Do,QUAS,Quantum-Stock,software,Medium,Romario Isaias Abreu Santos,6/21/2025 16:15,6/21/2025 16:21\n"

const jobJiraCSVNoBOMExport = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Priority,Reporter,Created,Updated\n" +
	"Agregar Producto,QUAS-1,10000,Task,To Do,QUAS,Quantum-Stock,software,Medium,Romario Isaias Abreu Santos,6/21/2025 16:14,6/21/2025 17:13\n" +
	"Desarrollo del Backend,QUAS-3,10002,Epic,To Do,QUAS,Quantum-Stock,software,Medium,Romario Isaias Abreu Santos,6/21/2025 16:15,6/21/2025 16:21\n"

// TestJobRow_JiraCSV_ABOMdExportIsNotReportedAsAFailedImport is the operator-facing half.
func TestJobRow_JiraCSV_ABOMdExportIsNotReportedAsAFailedImport(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// THE MUST-STAY-GREEN PREMISE, asserted first and on the SAME workspace shape: the identical
	// export without the three bytes imports both rows. Without this, a fixture that was broken for
	// some unrelated reason would make the BOM assertion below fail for the wrong cause and read as
	// a confirmed finding.
	control := importJiraCSVInto(t, d, ws.ID, team.ID, jobJiraCSVNoBOMExport)
	if control.Status != importer.JobSucceeded || control.Imported != 2 {
		t.Fatalf("PREMISE FAILED: the same export with no BOM read %s imported=%d skipped=%d failed=%d %q, "+
			"want succeeded/2. Nothing below is readable.",
			control.Status, control.Imported, control.Skipped, control.Failed, control.ErrorSummary)
	}

	ws2 := d.Workspace(t)
	team2 := d.Team(t, ws2.ID)
	job := importJiraCSVInto(t, d, ws2.ID, team2.ID, jobJiraCSVBOMdExport)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("job = %s imported=%d skipped=%d failed=%d %q, want succeeded/2.\n"+
			"The ONLY difference from the control above is three leading bytes, EF BB BF. The "+
			"operator is told the import FAILED with a real error count — the one outcome nobody "+
			"re-runs and nobody investigates as a Track bug.\n"+
			"MEASURED: 66 of 304 real Jira CSV exports in public repositories begin with those "+
			"bytes; 63 carry them on `Summary`, and in every one of those files the refusal is "+
			"100%% of the data.",
			job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}
	if n := issuesInWorkspace(t, d, ws2.ID); n != 2 {
		t.Errorf("issues in the workspace = %d, want 2 — the job row and the table must agree", n)
	}
}

// TestJobRow_JiraCSV_ABOMdExportLandsItsRowsUnderTheProviderKey is the routing half, and it is the
// assertion fakeIssueStore is structurally unable to make.
func TestJobRow_JiraCSV_ABOMdExportLandsItsRowsUnderTheProviderKey(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jobJiraCSVBOMdExport)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: job = %s imported=%d failed=%d %q, want succeeded/2 — this test is "+
			"measuring the wrong state", job.Status, job.Imported, job.Failed, job.ErrorSummary)
	}
	for _, key := range []string{"QUAS-1", "QUAS-3"} {
		var title string
		if err := d.Pool.QueryRow(ctx,
			`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`, ws.ID, key).Scan(&title); err != nil {
			t.Errorf("no issue addressable as %q: %v.\nThe rows landed but under Track-derived "+
				"identifiers, so a re-import of this export writes a second copy of every issue.", key, err)
		}
	}
}

// TestJobRow_JiraCSV_ReimportingABOMdExportDoesNotDuplicate closes the loop the routing half opens.
// #98 measured this for the un-BOM'd Jira export and fixed it; a BOM re-opens exactly that hole for
// one file in five, because the key column is read through the same index the BOM displaces.
func TestJobRow_JiraCSV_ReimportingABOMdExportDoesNotDuplicate(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	first := importJiraCSVInto(t, d, ws.ID, team.ID, jobJiraCSVBOMdExport)
	afterFirst := issuesInWorkspace(t, d, ws.ID)
	if first.Status != importer.JobSucceeded || afterFirst != 2 {
		t.Fatalf("PREMISE FAILED: first import = %s imported=%d, workspace holds %d issues, want "+
			"succeeded/2/2. A first import that landed nothing would satisfy the count below for "+
			"the worst possible reason.", first.Status, first.Imported, afterFirst)
	}

	second := importJiraCSVInto(t, d, ws.ID, team.ID, jobJiraCSVBOMdExport)
	if n := issuesInWorkspace(t, d, ws.ID); n != 2 {
		t.Errorf("after re-importing BYTE-IDENTICAL export bytes the workspace holds %d issues, want 2 "+
			"(second job: %s imported=%d skipped=%d failed=%d) — the backlog doubled and the job "+
			"reported itself clean.", n, second.Status, second.Imported, second.Skipped, second.Failed)
	}
}
