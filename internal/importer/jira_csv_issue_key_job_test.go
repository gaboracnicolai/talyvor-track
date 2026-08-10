package importer_test

// jira_csv_issue_key_job_test.go — the SAME column driven END TO END on real Postgres, through the
// shipped async runner and a jira_csv job, and read back OUT OF THE issues TABLE.
//
// ⚠ THIS IS NOT THE UNIT TEST TWICE, AND THE DIFFERENCE IS THE WHOLE FINDING. Identifier is not a
// column the mapper merely fills — it is the ROUTING KEY of source.go's write pipeline:
//
//	Identifier != ""  →  issue.Store.UpsertByIdentifier   INSERT-or-UPDATE on the provider key
//	Identifier == ""  →  issue.Store.Create               DERIVES <team>-<n>, discards the supplied key
//
// So a source-level assertion that the key reaches model.Issue can be green while the row still
// lands under a Track-derived identifier — and, far worse, while a re-import of the same file
// creates a second copy of every issue. THAT is what this file measures, and it is measured by
// COUNTING ROWS rather than by reading the mapper.
//
// MEASURED ON THIS FILE BEFORE THE FIX, through the runner on real Postgres: two jobs carrying
// BYTE-IDENTICAL two-row CSV bytes left FOUR issues in the workspace, and BOTH jobs reported
// `succeeded imported=2 skipped=0 failed=0`. A backlog silently doubled and reported clean.
//
// ⚠ WHY THIS TRANSPORT AND NOT THE OTHER ONE: jira_api has been idempotent since #71 — it carries
// the provider key, so a re-import UPDATEs. The two transports of the SAME provider had opposite
// re-import semantics, and the machinery the CSV half was missing (the upsert, #71's refuse-to-
// clobber-a-human predicate, #81's refused-vs-failed counting) was already built and tested. The
// CSV half simply never fed it.
//
// ⚠ THE LINEAR CSV HALF WAS DELIBERATELY LEFT HERE AND HAS SINCE BEEN MEASURED AND FIXED. This
// file's original note read "Linear's export header cannot be measured from this environment (no
// tenant, no anonymous export view)" — a true statement about this machine's ACCESS and a false one
// about the question. scripts/w34-linear-csv-export-probe.py reads 45 real Linear exports out of
// public repositories and finds `ID` at column index 0 in 45 of 45 across six header shapes. The
// refusal to GUESS was right; the conclusion that it could not be KNOWN was not. See
// linear_csv_issue_id.go, whose header states plainly why that provenance is weaker than this
// file's and is not dressed up as equal.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// A two-row export in the shape a real Jira CSV emits — `Issue key` first, exactly where the
// measured 279-column export puts it (see scripts/w34-jira-csv-export-probe.py).
const jiraCSVKeyedExport = "Issue key,Summary,Description,Status,Priority\n" +
	"JRASERVER-64802,Ticket one,body one,To Do,High\n" +
	"JRASERVER-78501,Ticket two,body two,Closed,Low\n"

// A CSV with NO key column: the fail-safe case. It must keep importing exactly as it did before.
const jiraCSVKeylessExport = "Summary,Description,Status,Priority\n" +
	"Keyless one,body one,To Do,High\n"

// importJiraCSVInto enqueues one jira_csv job for an existing workspace/team and drains it through
// the shipped runner. Returns the job row so the caller can assert on what the operator is told.
func importJiraCSVInto(t *testing.T, d *testutil.DB, wsID, teamID, body string) *importer.Job {
	t.Helper()
	ctx := context.Background()
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	id, err := js.Create(ctx, wsID, teamID, "jira_csv", []byte(body))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	did, err := runner.RunOnce(ctx)
	if err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	job, err := js.Get(ctx, id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return job
}

func issuesInWorkspace(t *testing.T, d *testutil.DB, wsID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM issues WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	return n
}

// TestJobRow_JiraCSV_TheIssueKeepsTheKeyJiraGaveIt is the column half: the row must be addressable
// by the name the provider — and every human, commit message and agent prompt — calls it.
func TestJobRow_JiraCSV_TheIssueKeepsTheKeyJiraGaveIt(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVKeyedExport)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: job = %s imported=%d skipped=%d failed=%d %q, want succeeded/2 — "+
			"this test is measuring the wrong state", job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}

	for _, key := range []string{"JRASERVER-64802", "JRASERVER-78501"} {
		var title string
		err := d.Pool.QueryRow(ctx,
			`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`, ws.ID, key).Scan(&title)
		if err != nil {
			t.Errorf("no issue addressable as %q after a jira_csv import: %v.\n"+
				"The export names every issue in its `Issue key` column and the row landed under a "+
				"Track-derived identifier instead.", key, err)
		}
	}
}

// TestJobRow_JiraCSV_ReimportingTheSameExportDoesNotDuplicate is the finding, measured by counting
// rows rather than by reading the mapper.
//
// ⚠ THE COUNT IS THE ASSERTION AND THE STATUS IS THE SECOND ONE. Before the fix BOTH jobs reported
// `succeeded imported=2 skipped=0 failed=0` while the workspace went from 2 issues to 4: an
// operator re-running yesterday's export to pick up a few new tickets was told the import was clean
// and got a second copy of their entire backlog.
func TestJobRow_JiraCSV_ReimportingTheSameExportDoesNotDuplicate(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	first := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVKeyedExport)
	afterFirst := issuesInWorkspace(t, d, ws.ID)
	// PREMISE, asserted rather than assumed: the first import really did land both rows. Without
	// this a fixture that imported NOTHING would satisfy the count assertion below for the worst
	// possible reason.
	if first.Status != importer.JobSucceeded || afterFirst != 2 {
		t.Fatalf("premise: first import = %s, %d issues; want succeeded, 2", first.Status, afterFirst)
	}

	second := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVKeyedExport)
	afterSecond := issuesInWorkspace(t, d, ws.ID)
	if afterSecond != afterFirst {
		t.Errorf("re-importing BYTE-IDENTICAL export bytes took the workspace from %d issues to %d.\n"+
			"Every row was written a second time under a fresh Track-derived identifier, and the job "+
			"reported %s imported=%d skipped=%d failed=%d — a duplicated backlog reported as a clean import.",
			afterFirst, afterSecond, second.Status, second.Imported, second.Skipped, second.Failed)
	}
}

// TestJobRow_JiraCSV_AReimportUpdatesTheRowItAlreadyWrote is the other half of idempotent, and it
// is separate on purpose: a re-import that duplicated nothing because it wrote nothing would pass
// the count assertion above. This one proves the second job actually landed its content.
func TestJobRow_JiraCSV_AReimportUpdatesTheRowItAlreadyWrote(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVKeyedExport)

	edited := "Issue key,Summary,Description,Status,Priority\n" +
		"JRASERVER-64802,Ticket one RETITLED IN JIRA,body one,To Do,High\n" +
		"JRASERVER-78501,Ticket two,body two,Closed,Low\n"
	job := importJiraCSVInto(t, d, ws.ID, team.ID, edited)

	var title string
	if err := d.Pool.QueryRow(ctx,
		`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`,
		ws.ID, "JRASERVER-64802").Scan(&title); err != nil {
		t.Fatalf("read back JRASERVER-64802: %v (job %s %q)", err, job.Status, job.ErrorSummary)
	}
	if title != "Ticket one RETITLED IN JIRA" {
		t.Errorf("title = %q, want the re-imported one — a second import that changes nothing is "+
			"not idempotence, it is a no-op", title)
	}
}

// TestJobRow_JiraCSV_AKeylessExportStillImports is the fail-safe. An export filtered down to a few
// columns carries no `Issue key`; it must import exactly as it did before this merge, under a
// Track-derived identifier, and must NOT be routed into the upsert on a fabricated key.
func TestJobRow_JiraCSV_AKeylessExportStillImports(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVKeylessExport)
	if job.Status != importer.JobSucceeded || job.Imported != 1 {
		t.Fatalf("keyless export = %s imported=%d skipped=%d failed=%d %q, want succeeded/1",
			job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}
	var ident string
	if err := d.Pool.QueryRow(ctx,
		`SELECT identifier FROM issues WHERE workspace_id=$1`, ws.ID).Scan(&ident); err != nil {
		t.Fatalf("read identifier: %v", err)
	}
	if ident == "" {
		t.Errorf("identifier is empty — a keyless row must still take a Track-derived key")
	}
}

// TestJobRow_JiraCSV_ARowAHumanOwnsIsRefusedNotOverwritten is #71's policy reaching this transport
// for the first time. Before the fix a CSV import could not overwrite a human's issue because it
// never landed on a human's identifier at all — it made a duplicate instead. Now that it lands on
// the provider key, the refusal that protects a native issue has to be shown to apply here too.
func TestJobRow_JiraCSV_ARowAHumanOwnsIsRefusedNotOverwritten(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// A human's issue occupying exactly the identifier the export is about to claim.
	if _, err := d.Pool.Exec(ctx,
		`INSERT INTO issues (workspace_id, team_id, number, identifier, title, creator_id)
		 VALUES ($1,$2,9001,'JRASERVER-64802','MINE, WRITTEN BY A PERSON','user-1')`,
		ws.ID, team.ID); err != nil {
		t.Fatalf("seed native issue: %v", err)
	}

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVKeyedExport)

	var title string
	if err := d.Pool.QueryRow(ctx,
		`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`,
		ws.ID, "JRASERVER-64802").Scan(&title); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if title != "MINE, WRITTEN BY A PERSON" {
		t.Errorf("the import overwrote a human's issue: title = %q", title)
	}
	// #81's counters: a refusal is not a failure, and it must be visible as its own number.
	if job.Skipped != 1 {
		t.Errorf("job.skipped (the REFUSED count) = %d, want 1 — status=%s imported=%d failed=%d %q",
			job.Skipped, job.Status, job.Imported, job.Failed, job.ErrorSummary)
	}
}
