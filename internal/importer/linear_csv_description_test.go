package importer

import (
	"context"
	"strings"
	"testing"
)

// linear_csv_description_test.go — the Linear CSV `Description` column is READ and was ASSERTED
// BY NOTHING.
//
// HOW IT WAS FOUND, AND WHY NOT BY READING. A column name is the join key between an export
// file and Track's model, and `columnIndex.get` returns "" — never an error — for a column the
// header does not carry. That is deliberate and documented ("Returns "" if the column doesn't
// exist OR THE ROW IS TOO SHORT"), and it is exactly what makes a wrong spelling silent: a
// column that stopped being read looks identical to a column the provider left blank. So the
// question is not "is the spelling right today" but "would anything go red if it were wrong",
// and that is knowable only by mutation.
//
// MEASURED at 633173e over all 25 column reads in the two CSV mappers, one at a time, each
// scored by SET SUBTRACTION against a measured C0 failing set rather than by an exit code
// (scripts/w34-csv-column-reach-controls-2q7v.py): 24 CAUGHT, **1 NOT CAUGHT — this one**, and
// the NOT CAUGHT verdict was re-asked of the WHOLE repository before being believed, because a
// guard for a mapper can live in another package.
//
// ⚠ THE MUTATION IS APPLIED AT THE READ SITE, NEVER AT A CONSTANT, AND THAT IS WHAT MAKES THE
// CENSUS HONEST. Nine of those columns are named by a package constant and the tests build
// their fixture headers from THOSE SAME CONSTANTS — so mutating the constant moves the lookup
// and the fixture together, the row still matches, and the site scores NOT CAUGHT for a reason
// that has nothing to do with coverage. Appending a suffix at the read site moves only the
// lookup, which is the real defect's shape.
//
// ⚠ THE ASYMMETRY IS THE POINT, AND IT IS ONE LINE WIDE. `Description: ci.get(row,
// "Description")` appears TWICE in csv.go, byte-identical: csv.go:828 on the Jira path and
// csv.go:602 on the Linear path. The Jira one is pinned by
// TestImportJiraCSV_ImportsIssuesCorrectly, which asserts Title AND Description. Its Linear
// twin — TestImportLinearCSV_ImportsThreeIssues, over a `linearCSV` fixture that SUPPLIES
// descriptions ("Cache invalidation is broken") — asserts WorkspaceID, TeamID and Title, and
// then stops. The value was fed in and never read back: a captured field with no assertion.
//
// ⚠ THIS IS A TRAP, NOT A LIVE DEFECT. The spelling is right today — `Description` is in all
// six real Linear header shapes the corpus probe found (linear_csv_issue_id_job_test.go:39).
// What was missing is the thing that would notice if it stopped being. Nothing in the product
// changes here.
//
// ⚠ WHY BOTH DIRECTIONS. "The description arrives" is satisfied by a mapper that copied ANY
// neighbouring cell, so the fixture below gives Description a value that matches no other cell
// in its row — that is the assertion that separates "reads the right column" from "reads a
// column". And the absent-column case is pinned too: without it, the obvious way to make the
// first test pass after a break is a fallback to Title, which would silently fabricate a
// description for every export that carries none.

// linearDescriptionCSV: every cell in the LIN-1 row is distinct, so an assertion on Description
// cannot be satisfied by the mapper having picked up a neighbour. `Status` and `Priority` are
// real Linear vocabulary so the row imports rather than being refused for an unmappable value.
const linearDescriptionCSV = `ID,Title,Description,Status,Priority,Assignee,Labels
LIN-1,alpha-title,beta-description,Backlog,Urgent,carol,gamma-label
`

func TestLinearCSV_TheDescriptionColumnReachesTheModel(t *testing.T) {
	imp, store := newTestImporter()
	if _, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1",
		strings.NewReader(linearDescriptionCSV)); err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("store got %d issues, want 1", len(store.created))
	}
	got := store.created[0]
	if got.Description != "beta-description" {
		t.Errorf("Description = %q, want \"beta-description\" — the Linear mapper's "+
			"`Description` read (csv.go:602) is the one column read of 25 that no assertion "+
			"covered; empty means the column is not being found, and another cell's value "+
			"means the mapper is reading the wrong column. Issue: %+v", got.Description, got)
	}
	// Discrimination: the description must not be any of its neighbours. This is what stops a
	// mapper that copied Title (or Labels, or Status) from satisfying the assertion above.
	if got.Description == got.Title {
		t.Errorf("Description equals Title (%q) — the fixture gives them different values, so "+
			"this means the mapper read the wrong column", got.Title)
	}
}

// The fail-safe half. An export filtered down past `Description` must import the row with an
// EMPTY description rather than refusing it or substituting a neighbour — the same
// absent-column contract every other read in these mappers has ("ABSENT => "" => EXACTLY
// TODAY'S BEHAVIOUR"). This is also what makes the test above non-vacuous in the other
// direction: with only that one, a fallback to Title would be a passing "fix".
func TestLinearCSV_AnExportWithNoDescriptionColumnImportsAnEmptyDescription(t *testing.T) {
	imp, store := newTestImporter()
	const noDescription = `ID,Title,Status,Priority,Labels
LIN-2,alpha-title,Backlog,Urgent,gamma-label
`
	out, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1",
		strings.NewReader(noDescription))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if out.Imported != 1 || out.Skipped != 0 {
		t.Fatalf("imported=%d skipped=%d, want 1/0 — an export with no Description column must "+
			"still import; errors: %v", out.Imported, out.Skipped, out.Errors)
	}
	if len(store.created) != 1 {
		t.Fatalf("store got %d issues, want 1", len(store.created))
	}
	if got := store.created[0].Description; got != "" {
		t.Errorf("Description = %q, want \"\" — the export carries no Description column, so "+
			"anything here was fabricated from another cell", got)
	}
}

// Cross-transport parity. `Description: ci.get(row, "Description")` is written out twice, once
// per mapper, and until now exactly one of the two copies was load-bearing under test. This
// drives the SAME description value through BOTH transports so the rule is pinned as a shared
// one: a change that lands the column on one path and not the other reds here rather than
// waiting to be noticed on whichever path nobody asserted.
func TestCSVDescription_BothTransportsLandTheSameColumn(t *testing.T) {
	// Same column, same value, one header spelling per provider (Jira names the key column
	// `Issue Key` and its title column `Summary`; Linear names them `ID` and `Title`). Only the
	// Description cell is being compared, and it is identical in both.
	const jiraBody = "Issue Key,Summary,Description,Status,Priority\n" +
		"KEY-1,alpha-title,beta-description,To Do,High\n"
	const linearBody = "ID,Title,Description,Status,Priority\n" +
		"LIN-1,alpha-title,beta-description,Backlog,Urgent\n"

	jiraImp, jiraStore := newTestImporter()
	if _, err := jiraImp.ImportJiraCSV(context.Background(), "ws-1", "team-1",
		strings.NewReader(jiraBody)); err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	linearImp, linearStore := newTestImporter()
	if _, err := linearImp.ImportLinearCSV(context.Background(), "ws-1", "team-1",
		strings.NewReader(linearBody)); err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}

	if len(jiraStore.created) != 1 || len(linearStore.created) != 1 {
		t.Fatalf("jira created %d, linear created %d, want 1 each",
			len(jiraStore.created), len(linearStore.created))
	}
	j, l := jiraStore.created[0].Description, linearStore.created[0].Description
	if j != "beta-description" || l != "beta-description" {
		t.Errorf("the two mappers disagree about the SAME `Description` column: jira=%q "+
			"linear=%q, both want \"beta-description\". The read is written out once per "+
			"mapper (csv.go:828 and csv.go:602) and this is the assertion that keeps the two "+
			"copies honest.", j, l)
	}
}
