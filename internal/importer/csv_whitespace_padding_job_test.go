package importer_test

// csv_whitespace_padding_job_test.go — THE TWO WHITESPACE TRIMS IN `columnIndex` ARE LOAD-BEARING
// FOR EVERY CSV IMPORT, AND BEFORE THIS FILE THE ENTIRE 39-PACKAGE SUITE WAS BLIND TO REMOVING
// EITHER OF THEM.
//
// The finding was handed on by tab-r8kw at the end of #188 and is reproduced here before being
// fixed: with `columnIndex.get`'s `strings.TrimSpace` deleted, `go test ./internal/importer/`
// stayed `ok` — 129 matched tests and the full suite both green on a real behaviour change in the
// CSV reader. Re-measured this session on `2cc81e9`, and the sibling trim in `buildIndex` measured
// for the first time: BOTH are silent.
//
// ⚠ WHAT THE TWO TRIMS ACTUALLY DECIDE — measured through the mappers, one mutation at a time,
// not read off the source:
//
//	buildIndex: k := TrimSpace(ToLower(h))     the HEADER key
//	    removed ⇒ a header written `ID,Team ," Title ",Description\t` — TRAILING space, a trailing
//	    tab, or a quoted field with either — stores the key "title " (or " title "),
//	    `get(row,"Title")` looks up "title", MISSES, the title is "", and errEmptyTitle rejects the
//	    row. Measured: EVERY row in the file rejected, imported=0.
//	    ⚠ LEADING whitespace on an unquoted field is NOT this trim's job and never reaches it —
//	    source.go:364 sets rd.TrimLeadingSpace, so encoding/csv has already removed it. The first
//	    draft of this file's fixture got that wrong and control C2 is what caught it; the
//	    measurement is quoted in full above wsPaddedHeaderLinearExport.
//
//	get: return TrimSpace(row[idxs[0]])        the CELL value
//	    removed ⇒ (a) `Identifier` becomes "  AWA-27  ", and Identifier is the ROUTING KEY of
//	    source.go's write pipeline, so the same issue re-imported without the padding is written a
//	    SECOND time instead of upserted; (b) a title cell of "   " is no longer "" so errEmptyTitle
//	    stops refusing it and a blank-titled issue lands; (c) titles and descriptions keep their
//	    padding. Measured through both mappers: title="  Fix login  ", id="  ENG-1  ".
//
// ⚠ WHY THESE ARE JOB TESTS AND NOT MAPPER UNIT TESTS. The same reason
// linear_csv_issue_id_job_test.go gives: Identifier is not a field the mapper merely fills, it
// selects between issue.Store.UpsertByIdentifier and issue.Store.Create. A mapper-level assertion
// on the string cannot see a duplicated backlog. TestPaddedIdentifier_UpsertsOntoTheRowItAlready…
// below measures it by COUNTING ROWS, and every test here reads back out of the issues table.
//
// ⚠ THE THIRD TRIM IS DELIBERATELY NOT GUARDED HERE, AND THE REASON IS A MEASUREMENT.
// `getAll`'s `TrimSpace` is ALSO silent to the whole suite, but its consequence is different in
// kind: `splitLabels` re-trims and drops empties, so Labels are UNAFFECTED — the only observable
// change is to three `len(getAll(...)) == 0` call sites in csv_issue_links.go,
// csv_dropped_objects.go and csv_custom_fields.go, i.e. to WARNING TEXT rather than to what lands
// in the database. That is a real gap and it is recorded rather than ridden on this diff: one
// merge per finding.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// The clean export: the measured real Linear column order (`ID` at index 0), two real keys out of
// the population linear_csv_issue_id.go censused.
const wsCleanLinearExport = "ID,Team,Title,Description,Status,Priority\n" +
	"AWA-27,Awaqi,Issue one,body one,Todo,High\n" +
	"SAN-617,Sanjiovani,Issue two,body two,Done,Low\n"

// The SAME two issues, byte-for-byte identical in MEANING, with every cell padded. Quoted, because
// encoding/csv preserves leading and trailing spaces inside quotes exactly as a real export does.
const wsPaddedCellsLinearExport = "ID,Team,Title,Description,Status,Priority\n" +
	"\"  AWA-27  \",\"  Awaqi  \",\"  Issue one  \",\"  body one  \",\"  Todo  \",\"  High  \"\n" +
	"\"  SAN-617  \",\"  Sanjiovani  \",\"  Issue two  \",\"  body two  \",\"  Done  \",\"  Low  \"\n"

// The same two issues again, cells clean, but the HEADER padded.
//
// ⚠⚠ THE PADDING IS TRAILING AND QUOTED, AND THAT IS A MEASUREMENT RATHER THAN A STYLE CHOICE.
// THE FIRST DRAFT OF THIS FIXTURE WROTE `ID, Team, Title` — a space after each comma — AND IT
// COULD NOT FAIL. source.go:364 sets `rd.TrimLeadingSpace = true`, so encoding/csv strips LEADING
// whitespace from an unquoted field before buildIndex is ever handed it. Measured against that
// exact reader config (FieldsPerRecord=-1, TrimLeadingSpace=true):
//
//	"ID, Title, Description"       -> ["ID" "Title" "Description"]       the READER removed it
//	"ID,Title ,Description "       -> ["ID" "Title " "Description "]      survives
//	"ID,\tTitle\t,Description"     -> ["ID" "Title\t" "Description"]      survives
//	"ID,\"Title \",Description"     -> ["ID" "Title " "Description"]       survives
//	"ID,\" Title \",Description"    -> ["ID" " Title " "Description"]      survives
//
// So a guard built on the first shape asserts about a header the product cannot receive: it stays
// green whether buildIndex trims or not. Control C2 predicted CAUGHT, got NOT, and that is the
// only reason this fixture is the shape it is. The header below therefore uses the three shapes
// that DO reach buildIndex — trailing space, trailing tab, and a quoted field with both.
const wsPaddedHeaderLinearExport = "ID,Team ,\" Title \",Description\t,Status ,Priority \n" +
	"AWA-27,Awaqi,Issue one,body one,Todo,High\n" +
	"SAN-617,Sanjiovani,Issue two,body two,Done,Low\n"

const wsPaddedHeaderJiraExport = "Issue key,\" Summary \",Description ,Status\t,Priority \n" +
	"PROJ-1,Issue one,body one,Done,High\n" +
	"PROJ-2,Issue two,body two,Done,Low\n"

// A row whose title is whitespace and nothing else, alongside one good row. The good row is the
// premise: without it a fixture that imported NOTHING would satisfy "no blank title landed" for
// the worst possible reason.
const wsWhitespaceTitleLinearExport = "ID,Team,Title,Description,Status,Priority\n" +
	"AWA-27,Awaqi,Issue one,body one,Todo,High\n" +
	"BLANK-1,Awaqi,\"   \",body two,Todo,High\n"

type importedIssue struct {
	identifier  string
	title       string
	description string
	status      string
	priority    int
}

func runCSVImport(t *testing.T, d *testutil.DB, wsID, teamID, sourceType, body string) *importer.Job {
	t.Helper()
	ctx := context.Background()
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	id, err := js.Create(ctx, wsID, teamID, sourceType, []byte(body))
	if err != nil {
		t.Fatalf("create %s job: %v", sourceType, err)
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

// issuesIn reads back what actually landed, ordered so two workspaces are comparable.
func issuesIn(t *testing.T, d *testutil.DB, wsID string) []importedIssue {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT identifier, title, description, status::text, priority
		   FROM issues WHERE workspace_id=$1 ORDER BY title, identifier`, wsID)
	if err != nil {
		t.Fatalf("read issues: %v", err)
	}
	defer rows.Close()
	var out []importedIssue
	for rows.Next() {
		var i importedIssue
		if err := rows.Scan(&i.identifier, &i.title, &i.description, &i.status, &i.priority); err != nil {
			t.Fatalf("scan issue: %v", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate issues: %v", err)
	}
	return out
}

// TestPaddedCells_LandTheSameIssuesAsACleanExport is the `get` half. Two workspaces, the same two
// issues, one export padded and one not: what reaches the issues table must be indistinguishable.
func TestPaddedCells_LandTheSameIssuesAsACleanExport(t *testing.T) {
	d := testutil.New(t)
	clean := d.Workspace(t)
	cleanTeam := d.Team(t, clean.ID)
	padded := d.Workspace(t)
	paddedTeam := d.Team(t, padded.ID)

	cleanJob := runCSVImport(t, d, clean.ID, cleanTeam.ID, "linear_csv", wsCleanLinearExport)
	paddedJob := runCSVImport(t, d, padded.ID, paddedTeam.ID, "linear_csv", wsPaddedCellsLinearExport)

	// PREMISE, asserted rather than assumed: both jobs really landed both rows. Two empty
	// workspaces compare equal.
	if cleanJob.Status != importer.JobSucceeded || cleanJob.Imported != 2 {
		t.Fatalf("premise: clean job = %s imported=%d skipped=%d %q, want succeeded/2",
			cleanJob.Status, cleanJob.Imported, cleanJob.Skipped, cleanJob.ErrorSummary)
	}
	if paddedJob.Status != importer.JobSucceeded || paddedJob.Imported != 2 {
		t.Fatalf("padded export reported %s imported=%d skipped=%d %q, want succeeded/2 — "+
			"a padded cell must not change whether a row imports at all",
			paddedJob.Status, paddedJob.Imported, paddedJob.Skipped, paddedJob.ErrorSummary)
	}

	got, want := issuesIn(t, d, padded.ID), issuesIn(t, d, clean.ID)
	if len(want) != 2 {
		t.Fatalf("premise: the clean workspace holds %d issues, want 2", len(want))
	}
	if len(got) != len(want) {
		t.Fatalf("padded export left %d issues, clean export left %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d landed differently from a padded export.\n got  %+v\n want %+v\n"+
				"columnIndex.get trims the cell; without that trim `identifier` keeps its padding — "+
				"and identifier is the routing key source.go chooses UpsertByIdentifier vs Create on.",
				i, got[i], want[i])
		}
	}
}

// TestPaddedIdentifier_UpsertsOntoTheRowItAlreadyWrote is the sharpest consequence of the `get`
// trim and the reason this is a job test: it is measured by COUNTING ROWS. A customer who exports
// twice — once from a tool that pads and once from one that does not — must end with one backlog,
// not two.
func TestPaddedIdentifier_UpsertsOntoTheRowItAlreadyWrote(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	first := runCSVImport(t, d, ws.ID, team.ID, "linear_csv", wsCleanLinearExport)
	afterFirst := len(issuesIn(t, d, ws.ID))
	if first.Status != importer.JobSucceeded || afterFirst != 2 {
		t.Fatalf("premise: first import = %s, %d issues; want succeeded, 2", first.Status, afterFirst)
	}

	second := runCSVImport(t, d, ws.ID, team.ID, "linear_csv", wsPaddedCellsLinearExport)
	afterSecond := len(issuesIn(t, d, ws.ID))
	if afterSecond != afterFirst {
		t.Errorf("re-importing the SAME two issues with padded cells took the workspace from %d "+
			"issues to %d, and the job reported %s imported=%d skipped=%d.\n"+
			"`  AWA-27  ` and `AWA-27` are the same issue to every human who reads the export; "+
			"they are two routing keys to source.go, so the whole backlog was written twice and "+
			"the operator was told the import was clean.",
			afterFirst, afterSecond, second.Status, second.Imported, second.Skipped)
	}
}

// TestWhitespaceOnlyTitle_IsRefusedNotImported is the errEmptyTitle half. `get`'s trim is what
// turns "   " into "" and therefore what makes the refusal happen at all; without it the guard
// stops guarding and a blank-titled issue lands in the customer's backlog.
func TestWhitespaceOnlyTitle_IsRefusedNotImported(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	job := runCSVImport(t, d, ws.ID, team.ID, "linear_csv", wsWhitespaceTitleLinearExport)
	got := issuesIn(t, d, ws.ID)

	// PREMISE: the good row DID import. Otherwise "no blank title landed" is true because
	// nothing landed.
	//
	// ⚠ THE STATUS IS `partial` AND THE COUNTER IS `Failed`, AND THIS TEST FIRST ASSERTED
	// `succeeded`/`Skipped` AND WAS WRONG. Measured rather than assumed after that red:
	// source.go tallies an unmappable row in `out.Skipped`, and runner.go maps `out.Skipped` onto
	// the job's FAILED column and `out.Refused` onto the job's SKIPPED column (runner.go:156,166).
	// A row errEmptyTitle rejects therefore reports as `partial … 1 row(s) failed`, with the
	// message "row has no title; skipping" — the word in the error and the column it lands in
	// disagree, which is worth knowing and is NOT what this file is fixing.
	if job.Status != importer.JobPartial || job.Imported != 1 {
		t.Fatalf("premise: job = %s imported=%d failed=%d %q, want partial/1 — the good row "+
			"must land or this test is measuring an empty workspace",
			job.Status, job.Imported, job.Failed, job.ErrorSummary)
	}
	if len(got) != 1 {
		t.Fatalf("workspace holds %d issues after importing one good row and one whitespace-only "+
			"title; want 1. Landed: %+v", len(got), got)
	}
	if got[0].title != "Issue one" {
		t.Errorf("the surviving issue is %+v; want the good row.\n"+
			"A title cell of \"   \" is a title of nothing. errEmptyTitle refuses it only because "+
			"columnIndex.get trims the cell to \"\" first — without that trim the row imports and "+
			"the customer gets an issue with no readable name.", got[0])
	}
	if job.Failed != 1 {
		t.Errorf("job reported failed=%d, want 1 — the whitespace-only row must be REFUSED and "+
			"COUNTED, not silently dropped and not silently imported", job.Failed)
	}
}

// TestPaddedHeader_StillFindsEveryColumn is the `buildIndex` half, and it is run over BOTH CSV
// transports because the two mappers name their title column differently (`Title` vs
// `Summary`+`Title` fallback) even though they share one columnIndex.
func TestPaddedHeader_StillFindsEveryColumn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sourceType string
		body       string
		keys       [2]string
	}{
		{"linear_csv", "linear_csv", wsPaddedHeaderLinearExport, [2]string{"AWA-27", "SAN-617"}},
		{"jira_csv", "jira_csv", wsPaddedHeaderJiraExport, [2]string{"PROJ-1", "PROJ-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testutil.New(t)
			ws := d.Workspace(t)
			team := d.Team(t, ws.ID)

			job := runCSVImport(t, d, ws.ID, team.ID, tc.sourceType, tc.body)
			if job.Status != importer.JobSucceeded || job.Imported != 2 || job.Skipped != 0 {
				t.Fatalf("a header written `%s` imported %d and skipped %d (%s, %q); want 2 and 0.\n"+
					"buildIndex trims the header name; without that trim the padded columns are "+
					"stored under keys nothing looks up, every row loses its title, and "+
					"errEmptyTitle skips the ENTIRE FILE while the job still reports success.",
					tc.name, job.Imported, job.Skipped, job.Status, job.ErrorSummary)
			}

			got := issuesIn(t, d, ws.ID)
			if len(got) != 2 {
				t.Fatalf("workspace holds %d issues; want 2. Landed: %+v", len(got), got)
			}
			// The padded columns are not only present, they carry their values: a header that
			// resolved but a body that did not would pass a count-only assertion.
			for i, want := range [2]string{"Issue one", "Issue two"} {
				if got[i].title != want {
					t.Errorf("issue %d title = %q, want %q", i, got[i].title, want)
				}
				if got[i].description == "" {
					t.Errorf("issue %d (%q) has an empty description — the padded `Description\t` "+
						"column resolved to nothing", i, got[i].title)
				}
			}
			// The padded `Status ` column must resolve too. A row whose status column is not found
			// still imports — it falls back to backlog with a note — so title/description
			// assertions alone cannot see that column go missing.
			if got[1].status != "done" {
				t.Errorf("issue %q status = %q, want done — the padded `Status ` column did not "+
					"resolve, and an unfound status column silently imports every row as backlog",
					got[1].title, got[1].status)
			}
			for _, k := range tc.keys {
				found := false
				for _, g := range got {
					if g.identifier == k {
						found = true
					}
				}
				if !found {
					t.Errorf("no issue addressable as %q; landed %+v", k, got)
				}
			}
		})
	}
}
