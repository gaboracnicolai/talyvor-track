package importer

// linear_csv_issue_id_test.go — the SOURCE half of "a Linear CSV export names every issue and the
// mapper never asked".
//
// A Linear CSV export's first column is `ID` — AWA-27, the thing every human, every commit message
// and every agent prompt in that workspace calls the issue. linearRowMapper read Title,
// Description, Status, Priority, Labels, Created and Completed, and never this one, so every
// linear_csv row reached the write pipeline with `Identifier == ""`.
//
// That empty string is not a cosmetic loss: it is the ROUTING KEY of the write pipeline.
// source.go's run() sends a row carrying an Identifier through issue.Store.UpsertByIdentifier
// (INSERT-or-UPDATE on the provider key, with #71's refuse-to-clobber-a-human predicate) and a row
// without one through issue.Store.Create, which DERIVES `<team>-<n>` and discards whatever the
// caller supplied — see linear_csv_issue_id_job_test.go for what that costs, measured on real
// Postgres.

import "testing"

// realLinearCSVIssueIDs are values read off REAL exports, not invented: four different tenants out
// of the 45-file population scripts/w34-linear-csv-export-probe.py measures. Recorded here because
// a source-derived assertion cannot see a deletion, and because the SHAPE of the value (a team
// prefix, a hyphen, an integer) is part of the claim that this column is the issue's name rather
// than a surrogate.
var realLinearCSVIssueIDs = []string{
	"AWA-27",   // Amanuel-Ayal3w/Awaqi
	"SAN-617",  // amo-tech-ai/mdeapp
	"KAP-5",    // kapishdima/monica
	"PLAN-382", // null-hype/null-hype.github.io
}

func mapOneLinearCSVRow(t *testing.T, header, row []string) mappedIssue {
	t.Helper()
	got, err := linearRowMapper(buildIndex(header), row)
	if err != nil {
		t.Fatalf("linearRowMapper: %v", err)
	}
	return got
}

// TestLinearCSVIssueID_TheMeasuredSpelling pins the header spelling by hand.
//
// ⚠ HARDCODED, NOT READ FROM THE CONSTANT UNDER TEST. #75's C6: an assertion that compares the
// constant to itself passes for every possible value, including "".
func TestLinearCSVIssueID_TheMeasuredSpelling(t *testing.T) {
	if linearCSVIssueIDColumn != "ID" {
		t.Errorf("column spelling = %q, want %q — measured at index 0 of 45 of 45 real Linear "+
			"exports across six header shapes", linearCSVIssueIDColumn, "ID")
	}
}

// TestLinearCSVIssueID_ReachesTheModel is the positive case: the key a real export emits arrives in
// model.Issue.Identifier, which is what routes the row to the upsert.
func TestLinearCSVIssueID_ReachesTheModel(t *testing.T) {
	for _, key := range realLinearCSVIssueIDs {
		got := mapOneLinearCSVRow(t,
			[]string{"ID", "Team", "Title", "Status", "Priority"},
			[]string{key, "A team", "A real issue", "Done", "High"})
		if got.issue.Identifier != key {
			t.Errorf("Identifier = %q, want %q — the export names the issue and the mapper dropped it",
				got.issue.Identifier, key)
		}
	}

	// Matched case-INSENSITIVELY, which buildIndex already promises.
	lower := mapOneLinearCSVRow(t,
		[]string{"id", "team", "title", "status", "priority"},
		[]string{"ENG-7", "A team", "A real issue", "Done", "High"})
	if lower.issue.Identifier != "ENG-7" {
		t.Errorf("lowercased header: Identifier = %q, want %q", lower.issue.Identifier, "ENG-7")
	}
}

// TestLinearCSVIssueID_AbsentColumnStillImports is the fail-safe half, and it is the reason this
// change cannot break an export that has been filtered down to a handful of columns. No `ID` ⇒
// Identifier stays "" ⇒ run() takes the Create branch exactly as it did before this merge.
//
// ⚠ ITS OWN TEST FUNCTION, NOT A THIRD BLOCK ABOVE. When a positive and a negative case share a
// function the positive's t.Fatalf can fire first and the negative assertion is never evaluated —
// the failure mode #76's C1 and this package's own date controls both recorded.
//
// ⚠ IT IS NOT INDEPENDENTLY EARNED COVERAGE, AND THIS SAYS SO RATHER THAN HIDING IT — #98's C5
// lesson, inherited on purpose. This and TestLinearCSVIssueID_ANeighbouringIDColumnIsNotRead
// assert the SAME postcondition (Identifier == "") on two keyless fixtures, and THIS one's fixture
// is a strict subset of that one's, so no mutation can red this without redding that too. C4 in
// scripts/w34-linear-csv-issue-id-controls.py — a mapper that matches `ID` by substring — is the
// mutation that reds the neighbour test ALONE, and it is what justifies the pair. This function is
// kept as the plain-language statement of the property.
func TestLinearCSVIssueID_AbsentColumnStillImports(t *testing.T) {
	got := mapOneLinearCSVRow(t,
		[]string{"Title", "Status", "Priority"},
		[]string{"Keyless row", "Done", "High"})
	if got.issue.Identifier != "" {
		t.Errorf("Identifier = %q with no ID column, want \"\" — an invented key would route a "+
			"keyless row into the upsert and land it on a fabricated provider identifier",
			got.issue.Identifier)
	}
	if got.issue.Title != "Keyless row" {
		t.Errorf("Title = %q — the row must still map", got.issue.Title)
	}
}

// TestLinearCSVIssueID_ANeighbouringIDColumnIsNotRead is the NEGATIVE half of the spelling, and it
// carries more weight here than its Jira twin does because the header is two characters long.
//
// A real 34-column export carries `Project ID`, `Project Milestone ID` and `UUID` alongside `ID`.
// Every one of those lowercases to a string CONTAINING "id". A mapper that matched by substring
// would satisfy every positive assertion above while keying the entire backlog off a project — or,
// worse, off the `UUID` column, which holds a genuinely stable per-issue provider key and would
// therefore produce an import that LOOKS correct and is addressable by nothing any human types.
func TestLinearCSVIssueID_ANeighbouringIDColumnIsNotRead(t *testing.T) {
	got := mapOneLinearCSVRow(t,
		[]string{"Project ID", "Project Milestone ID", "UUID", "Title", "Status", "Priority"},
		[]string{"proj_9f31", "mile_22a8", "1395fd10-02bc-4207-a3b2-17c08e96bd7e", "A real issue", "Done", "High"})
	if got.issue.Identifier != "" {
		t.Errorf("Identifier = %q — read out of a column that is not %q",
			got.issue.Identifier, linearCSVIssueIDColumn)
	}
}

// TestLinearCSVIssueID_TheJiraColumnIsNotReadHere holds the two providers' spellings apart. A Linear
// export has no `Issue key` column and a Jira export has no bare `ID` column; a mapper that read
// both would be reaching for a column its own provider never emits, and the first sign of it would
// be a Jira export imported through the Linear endpoint keying off something unexpected.
func TestLinearCSVIssueID_TheJiraColumnIsNotReadHere(t *testing.T) {
	got := mapOneLinearCSVRow(t,
		[]string{"Issue key", "Title", "Status", "Priority"},
		[]string{"JRASERVER-64802", "A real issue", "Done", "High"})
	if got.issue.Identifier != "" {
		t.Errorf("Identifier = %q — linearRowMapper read Jira's column name", got.issue.Identifier)
	}
}
