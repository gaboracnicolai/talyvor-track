package importer

// jira_csv_issue_key_test.go — the SOURCE half of "a Jira CSV export names every issue and the
// mapper never asked".
//
// A Jira CSV export's first-class column is `Issue key` — PROJ-123, the thing every human, every
// commit message and every agent prompt calls the issue. jiraRowMapper read Summary, Description,
// Status, Priority, Labels, Resolution and four dates, and never this one, so every CSV-imported
// row reached the write pipeline with `Identifier == ""`.
//
// That empty string is not a cosmetic loss: it is the ROUTING KEY of the write pipeline.
// source.go's run() sends a row carrying an Identifier through issue.Store.UpsertByIdentifier
// (INSERT-or-UPDATE on the provider key, with #71's refuse-to-clobber-a-human predicate) and a row
// without one through issue.Store.Create, which DERIVES `<team>-<n>` and discards whatever the
// caller supplied. So the CSV transport could not reach the re-import machinery the API transport
// has used since #71 — see jira_csv_issue_key_job_test.go for what that costs, measured.

import "testing"

// realJiraCSVIssueKeys are values read off a REAL export, not invented: the same
// jira.atlassian.com JRASERVER export scripts/w34-jira-csv-export-probe.py downloads (279 columns,
// `Issue key` among them). Recorded here because a source-derived assertion cannot see a deletion.
var realJiraCSVIssueKeys = []string{
	"JRASERVER-64802",
	"JRASERVER-78501",
	"JRASERVER-76406",
	"JRASERVER-45903",
}

// TestJiraCSVIssueKey_TheMeasuredSpelling pins the header spelling by hand.
//
// ⚠ HARDCODED, NOT READ FROM THE CONSTANT UNDER TEST. #75's C6: an assertion that compares the
// constant to itself passes for every possible value, including "".
func TestJiraCSVIssueKey_TheMeasuredSpelling(t *testing.T) {
	if jiraCSVIssueKeyColumn != "Issue key" {
		t.Errorf("column spelling = %q, want %q — measured on the real export's 279 headers",
			jiraCSVIssueKeyColumn, "Issue key")
	}
}

// TestJiraCSVIssueKey_ReachesTheModel is the positive case: the key a real export emits arrives in
// model.Issue.Identifier, which is what routes the row to the upsert.
func TestJiraCSVIssueKey_ReachesTheModel(t *testing.T) {
	for _, key := range realJiraCSVIssueKeys {
		got := mapOneJiraCSVRow(t,
			[]string{"Issue key", "Summary", "Status", "Priority"},
			[]string{key, "A real issue", "Closed", "High"})
		if got.issue.Identifier != key {
			t.Errorf("Identifier = %q, want %q — the export names the issue and the mapper dropped it",
				got.issue.Identifier, key)
		}
	}

	// Matched case-INSENSITIVELY, which buildIndex already promises: Jira renders the field as
	// "Issue Key" in some views and the export header follows the field's display name.
	lower := mapOneJiraCSVRow(t,
		[]string{"issue key", "summary", "status", "priority"},
		[]string{"PROJ-7", "A real issue", "Closed", "High"})
	if lower.issue.Identifier != "PROJ-7" {
		t.Errorf("lowercased header: Identifier = %q, want %q", lower.issue.Identifier, "PROJ-7")
	}
}

// TestJiraCSVIssueKey_AbsentColumnStillImports is the fail-safe half, and it is the reason this
// change cannot break an export that has been filtered down to a handful of columns. No key ⇒
// Identifier stays "" ⇒ run() takes the Create branch exactly as it did before this merge.
//
// ⚠ ITS OWN TEST FUNCTION, NOT A THIRD BLOCK ABOVE. When a positive and a negative case share a
// function the positive's t.Fatalf can fire first and the negative assertion is never evaluated —
// the failure mode #76's C1 and this package's own date controls both recorded.
//
// ⚠ IT IS NOT INDEPENDENTLY EARNED COVERAGE, AND THE CONTROL CAMPAIGN SAYS SO RATHER THAN HIDING
// IT. This and TestJiraCSVIssueKey_ANeighbouringKeyColumnIsNotRead assert the SAME postcondition
// (Identifier == "") on two keyless fixtures, and THIS one's fixture is a strict subset of that
// one's — so no mutation can red this without redding that too. C5 in
// scripts/w34-jira-csv-issue-key-controls.py is that mutation and is recorded as justifying
// NEITHER catcher; C4, which reds the neighbour test ALONE, is what justifies the pair. This
// function is kept as the plain-language statement of the property.
func TestJiraCSVIssueKey_AbsentColumnStillImports(t *testing.T) {
	got := mapOneJiraCSVRow(t,
		[]string{"Summary", "Status", "Priority"},
		[]string{"Keyless row", "Closed", "High"})
	if got.issue.Identifier != "" {
		t.Errorf("Identifier = %q with no key column, want \"\" — an invented key would route a "+
			"keyless row into the upsert and land it on a fabricated provider identifier",
			got.issue.Identifier)
	}
	if got.issue.Title != "Keyless row" {
		t.Errorf("Title = %q — the row must still map", got.issue.Title)
	}
}

// TestJiraCSVIssueKey_ANeighbouringKeyColumnIsNotRead is the NEGATIVE half of the spelling.
// A real export carries `Issue id` (the numeric surrogate, e.g. 1284563) RIGHT NEXT TO `Issue key`,
// and `Parent key` a few columns further on. A mapper that matched anything containing "key" would
// satisfy every assertion above while writing the parent's identifier onto the child.
func TestJiraCSVIssueKey_ANeighbouringKeyColumnIsNotRead(t *testing.T) {
	got := mapOneJiraCSVRow(t,
		[]string{"Issue id", "Parent key", "Summary", "Status", "Priority"},
		[]string{"1284563", "PROJ-100", "A real issue", "Closed", "High"})
	if got.issue.Identifier != "" {
		t.Errorf("Identifier = %q — read out of a column that is not %q",
			got.issue.Identifier, jiraCSVIssueKeyColumn)
	}
}
