package importer_test

// linear_csv_issue_id_job_test.go — the column a Linear CSV export uses to NAME an issue, driven
// END TO END on real Postgres through the shipped async runner and a linear_csv job, and read back
// OUT OF THE issues TABLE.
//
// ⚠ THIS IS #98 ON THE TRANSPORT #98 DELIBERATELY LEFT. jira_csv got the provider key in `4b4b702`;
// linear_csv did not, and its own block said why: "Linear's export header cannot be measured from
// this environment". THAT STOP REASON IS NOW FALSE AND THE MEASUREMENT IS IN THE REPO —
// scripts/w34-linear-csv-export-probe.py reads 45 REAL Linear CSV exports out of public
// repositories, from 16 distinct team prefixes across unrelated tenants, and `ID` is column index 0
// in 45 of 45 files across SIX different header shapes (29, 30 and 34 columns). See
// linear_csv_issue_id.go for the full provenance and for why it is second-hand bytes rather than
// first-hand ones.
//
// ⚠ WHY A JOB TEST AND NOT ONLY A UNIT TEST, restated because it is the whole finding. Identifier is
// not a column the mapper merely fills — it is the ROUTING KEY of source.go's write pipeline:
//
//	Identifier != ""  →  issue.Store.UpsertByIdentifier   INSERT-or-UPDATE on the provider key
//	Identifier == ""  →  issue.Store.Create               DERIVES <team>-<n>, discards the key
//
// So every source-level assertion can be green while a re-import writes a second copy of every row.
// This file measures it by COUNTING ROWS.
//
// MEASURED ON THIS FILE BEFORE THE FIX, through the runner on real Postgres: two jobs carrying
// BYTE-IDENTICAL two-row Linear export bytes left FOUR issues in the workspace, and BOTH jobs
// reported `succeeded imported=2 skipped=0 failed=0`.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// A two-row export in the shape a real Linear CSV emits: `ID` FIRST, then `Team`, `Title`,
// `Description`, `Status` — the measured column order of all six header shapes the probe found. The
// keys are real ones out of the measured population (AWA-27 from Amanuel-Ayal3w/Awaqi, SAN-617 from
// amo-tech-ai/mdeapp).
const linearCSVKeyedExport = "ID,Team,Title,Description,Status,Priority,Labels\n" +
	"AWA-27,Awaqi,Issue one,body one,Todo,High,bug\n" +
	"SAN-617,Sanjiovani,Issue two,body two,Done,Low,chore\n"

// The SAME two issues with a column set that carries no `ID` — the fail-safe case. An export
// filtered down to a few columns must import exactly as it did before this merge.
const linearCSVKeylessExport = "Title,Description,Status,Priority,Labels\n" +
	"Keyless one,body one,Todo,High,bug\n"

// ⚠ THE NEIGHBOUR FIXTURE, AND IT MATTERS MORE HERE THAN IT DID FOR JIRA. `ID` is a two-character
// header and the SAME export carries three other columns whose lowercased names CONTAIN it:
// `Project ID`, `Project Milestone ID` and `UUID` (uuid). buildIndex matches the FULL header
// case-insensitively and never by substring — this fixture is what proves a mapper that got
// generous about that would be caught, and it is the only fixture in this file where reading the
// wrong column produces a plausible-looking value instead of an obvious one.
const linearCSVNeighboursOnlyExport = "Project ID,Project Milestone ID,UUID,Title,Description,Status,Priority\n" +
	"proj_9f31,mile_22a8,1395fd10-02bc-4207-a3b2-17c08e96bd7e,Neighbours only,body,Todo,High\n"

// importLinearCSVInto enqueues one linear_csv job for an existing workspace/team and drains it
// through the shipped runner. Returns the job row so the caller can assert on what the operator is
// told, not only on what the database holds.
func importLinearCSVInto(t *testing.T, d *testutil.DB, wsID, teamID, body string) *importer.Job {
	t.Helper()
	ctx := context.Background()
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	id, err := js.Create(ctx, wsID, teamID, "linear_csv", []byte(body))
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

func linearIssuesInWorkspace(t *testing.T, d *testutil.DB, wsID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM issues WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	return n
}

// TestJobRow_LinearCSV_TheIssueKeepsTheKeyLinearGaveIt is the column half: after a linear_csv
// import the row must be addressable by the name Linear — and every human, commit message and agent
// prompt in that workspace — calls it.
func TestJobRow_LinearCSV_TheIssueKeepsTheKeyLinearGaveIt(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVKeyedExport)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("premise: job = %s imported=%d skipped=%d failed=%d %q, want succeeded/2 — "+
			"this test is measuring the wrong state", job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}

	for _, key := range []string{"AWA-27", "SAN-617"} {
		var title string
		err := d.Pool.QueryRow(ctx,
			`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`, ws.ID, key).Scan(&title)
		if err != nil {
			t.Errorf("no issue addressable as %q after a linear_csv import: %v.\n"+
				"A Linear export names every issue in its `ID` column — measured at index 0 of 45 of 45 "+
				"real exports — and the row landed under a Track-derived identifier instead.", key, err)
		}
	}
}

// TestJobRow_LinearCSV_ReimportingTheSameExportDoesNotDuplicate is the finding, measured by counting
// rows rather than by reading the mapper.
//
// ⚠ THE COUNT IS THE ASSERTION AND THE JOB STATUS IS THE SECOND ONE. Before the fix BOTH jobs
// reported `succeeded imported=2 skipped=0 failed=0` while the workspace went from 2 issues to 4:
// an operator re-running yesterday's export to pick up a few new tickets was told the import was
// clean and got a second copy of their entire backlog.
func TestJobRow_LinearCSV_ReimportingTheSameExportDoesNotDuplicate(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	first := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVKeyedExport)
	afterFirst := linearIssuesInWorkspace(t, d, ws.ID)
	// PREMISE, asserted rather than assumed: the first import really did land both rows. Without
	// this a fixture that imported NOTHING would satisfy the count assertion below for the worst
	// possible reason.
	if first.Status != importer.JobSucceeded || afterFirst != 2 {
		t.Fatalf("premise: first import = %s, %d issues; want succeeded, 2", first.Status, afterFirst)
	}

	second := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVKeyedExport)
	afterSecond := linearIssuesInWorkspace(t, d, ws.ID)
	if afterSecond != afterFirst {
		t.Errorf("re-importing BYTE-IDENTICAL Linear export bytes took the workspace from %d issues to %d.\n"+
			"Every row was written a second time under a fresh Track-derived identifier, and the job "+
			"reported %s imported=%d skipped=%d failed=%d — a duplicated backlog reported as a clean import.",
			afterFirst, afterSecond, second.Status, second.Imported, second.Skipped, second.Failed)
	}
}

// TestJobRow_LinearCSV_AReimportUpdatesTheRowItAlreadyWrote is the other half of idempotent, and it
// is a separate test on purpose: a re-import that duplicated nothing because it wrote nothing would
// pass the count assertion above. This one proves the second job actually landed its content.
func TestJobRow_LinearCSV_AReimportUpdatesTheRowItAlreadyWrote(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVKeyedExport)

	edited := "ID,Team,Title,Description,Status,Priority,Labels\n" +
		"AWA-27,Awaqi,Issue one RETITLED IN LINEAR,body one,Todo,High,bug\n" +
		"SAN-617,Sanjiovani,Issue two,body two,Done,Low,chore\n"
	job := importLinearCSVInto(t, d, ws.ID, team.ID, edited)

	var title string
	if err := d.Pool.QueryRow(ctx,
		`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`,
		ws.ID, "AWA-27").Scan(&title); err != nil {
		t.Fatalf("read back AWA-27: %v (job %s %q)", err, job.Status, job.ErrorSummary)
	}
	if title != "Issue one RETITLED IN LINEAR" {
		t.Errorf("title = %q, want the re-imported one — a second import that changes nothing is "+
			"not idempotence, it is a no-op", title)
	}
}

// TestJobRow_LinearCSV_AKeylessExportStillImports is the fail-safe. An export filtered down to a few
// columns carries no `ID`; it must import exactly as it did before this merge, under a Track-derived
// identifier, and must NOT be routed into the upsert on a fabricated key.
func TestJobRow_LinearCSV_AKeylessExportStillImports(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVKeylessExport)
	if job.Status != importer.JobSucceeded || job.Imported != 1 {
		t.Fatalf("keyless export = %s imported=%d skipped=%d failed=%d %q, want succeeded/1",
			job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}
	var ident string
	if err := d.Pool.QueryRow(ctx,
		`SELECT identifier FROM issues WHERE workspace_id=$1`, ws.ID).Scan(&ident); err != nil {
		t.Fatalf("read identifier: %v", err)
	}
	// ⚠ THE EXACT DERIVED KEY, NOT MERELY "NOT EMPTY" — AND A CONTROL IS WHY. This assertion first
	// read `ident == ""`, and C5 of scripts/w34-linear-csv-issue-id-controls.py (a mapper that
	// invents a key for a keyless row) walked straight past it: "FABRICATED-1" is not empty, so a
	// row routed into the upsert on a made-up provider identifier satisfied the fail-safe test that
	// exists to prevent exactly that. The prediction that this test would catch C5 was WRONG, and
	// the miss is the reason the assertion is now the derived key itself.
	want := team.Identifier + "-1"
	if ident != want {
		t.Errorf("identifier = %q, want the Track-derived %q — a keyless row must take a derived "+
			"key, and must NOT be routed into the upsert on an invented provider identifier", ident, want)
	}
}

// TestJobRow_LinearCSV_ARowAHumanOwnsIsRefusedNotOverwritten is #71's policy reaching this transport
// for the first time. Before this merge a linear_csv import could not overwrite a human's issue
// because it never landed on a human's identifier at all — it made a duplicate instead. Now that it
// lands on the provider key, the refusal that protects a native issue has to be shown to apply here.
func TestJobRow_LinearCSV_ARowAHumanOwnsIsRefusedNotOverwritten(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// A human's issue occupying exactly the identifier the export is about to claim.
	if _, err := d.Pool.Exec(ctx,
		`INSERT INTO issues (workspace_id, team_id, number, identifier, title, creator_id)
		 VALUES ($1,$2,9001,'AWA-27','MINE, WRITTEN BY A PERSON','user-1')`,
		ws.ID, team.ID); err != nil {
		t.Fatalf("seed native issue: %v", err)
	}

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVKeyedExport)

	var title string
	if err := d.Pool.QueryRow(ctx,
		`SELECT title FROM issues WHERE workspace_id=$1 AND identifier=$2`,
		ws.ID, "AWA-27").Scan(&title); err != nil {
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

// TestJobRow_LinearCSV_TheNeighbouringIDColumnsAreNotTheKey is the neighbour control, driven to the
// database rather than asserted at the mapper — a mapper that matched `ID` by substring would pick
// up `Project ID`, `Project Milestone ID` or `UUID`, and every one of those produces an identifier
// that LOOKS like a provider key while addressing the wrong thing (or, for the two project columns,
// while making every issue in a project collide on one identifier).
func TestJobRow_LinearCSV_TheNeighbouringIDColumnsAreNotTheKey(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := importLinearCSVInto(t, d, ws.ID, team.ID, linearCSVNeighboursOnlyExport)
	if job.Status != importer.JobSucceeded || job.Imported != 1 {
		t.Fatalf("premise: job = %s imported=%d %q, want succeeded/1", job.Status, job.Imported, job.ErrorSummary)
	}

	var ident string
	if err := d.Pool.QueryRow(ctx,
		`SELECT identifier FROM issues WHERE workspace_id=$1`, ws.ID).Scan(&ident); err != nil {
		t.Fatalf("read identifier: %v", err)
	}
	for _, wrong := range []string{"proj_9f31", "mile_22a8", "1395fd10-02bc-4207-a3b2-17c08e96bd7e"} {
		if ident == wrong {
			t.Errorf("identifier = %q — the mapper read a NEIGHBOURING column whose name merely "+
				"contains \"id\". `ID` is matched as a FULL header, not as a substring.", ident)
		}
	}
	// ⚠ AND THE POSITIVE STATEMENT, not only the three names. An enumerated exclusion list is not a
	// predicate — it can only ever see the values somebody thought to write down. This row carries
	// no `ID`, so the only correct outcome is the Track-derived key.
	if want := team.Identifier + "-1"; ident != want {
		t.Errorf("identifier = %q, want the Track-derived %q — some column that is not `ID` "+
			"reached model.Issue.Identifier and routed this row into the upsert", ident, want)
	}
}
