package importer

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// jira_csv_status_category_test.go — the CANONICAL, non-renameable state Jira writes into its own
// CSV export, in a column this package has said four separate times does not exist.
//
// ⚠⚠ THE PREMISE WAS WRITTEN DOWN FOUR TIMES AND NOBODY MEASURED IT. All four sentences below were
// in this repository before this merge, all four were false, and this merge corrects each of them
// where it stands rather than only adding the missing read:
//
//	csv.go, FieldNote.Via         "Via  \"\" (no fallback exists for this transport — every CSV path)"
//	csv.go, statusFallback        "which is every CSV path — a Jira CSV export carries no category
//	                               column, so those warnings keep their existing wording exactly"
//	jira.go, mapJiraIssues        "a route the CSV mapper does not have"
//	status_category_test.go       "The CSV transports have no category ... a Jira CSV export carries
//	                               no such column, so claiming \"no statusCategory present\" there
//	                               would be a sentence about a field that was never in play"
//
// MEASURED WHOLE-POPULATION over the 304-file real-Jira-CSV corpus #103 cached at
// /tmp/w34-jira-corpus (real exports committed to PUBLIC repositories by unrelated instances; the
// same instrument #99 built for Linear and #103 first pointed at Jira). Read as RAW BYTES, ragged
// rows counted rather than discarded, and every classification below answered by THIS PACKAGE's own
// mapJiraStatus / mapJiraStatusCategory rather than by a transcription of them:
//
//	Jira CSV exports in the corpus                                    304
//	  carrying a "Status Category" column                             228   (75.0%)
//	data rows                                                      17,657
//	  Status NAME recognised by mapJiraStatus                      14,490   (82.1%)
//	  Status NAME unrecognised ⇒ imported as backlog                3,167   (17.9%)
//	     ... with a Status Category cell that says what it IS        1,424
//	           "To Do"        786
//	           "In Progress"  508
//	           "Done"         130   ← 129 of these also carry a Resolved date
//	     ... with no Status Category cell at all                     1,743
//	     ... category present and still unresolvable                     0
//	files with ≥1 such row  57 of 304 · ≥50% of the file's rows 23 · 100% of the file's rows 5
//
// ⚠⚠ THE CSV COLUMN CARRIES THE DISPLAY NAME, NOT THE API KEY, AND THAT IS THE WHOLE TRAP. The
// shipped mapJiraStatusCategory reads Jira's four category KEYS (`new`/`indeterminate`/`done`/
// `undefined`, measured off /rest/api/2/statuscategory by #73). The CSV column spells the same four
// as their DISPLAY NAMES. Reusing that function verbatim on this column — the obvious fix, and the
// one this file exists to stop — resolves 130 of the 1,424 rows and silently misses 1,294, because
// "Done" happens to collide with its own key while "To Do" and "In Progress" do not.
// The corpus's whole category vocabulary is four values: `To Do` 5,831 · `Done` 4,037 ·
// `In Progress` 1,414 · `new` 2 — so the key spellings DO occur in the wild and both are read.
//
// ⚠ NAME FIRST, CATEGORY SECOND — the same order jira.go uses, and the corpus says why it matters:
// 373 rows carry a recognised status name whose category DISAGREES with it, and the disagreements
// are the dangerous direction — `Won't Do`/`Canceled` (34 rows) sit in category `Done`, so a
// category-first mapper imports abandoned work as delivered.
//
// ⚠ THE SECOND LOSS IS THE ONE THAT REACHES A PUBLISHED NUMBER. jiraCSVResolved gates the completion
// time on the row importing as `done`; a Done-category row that imported as backlog therefore had
// its Resolved date REFUSED as well, reported as "the issue imported as backlog". 129 of the 130
// Done-category rows in the corpus carry a Resolved date. Two losses, one cause.

// The fixture is FOUR REAL (Status, Status Category) PAIRS taken from the corpus, from unrelated
// instances — not invented. Row counts across the 304 files: "New"/To Do 446 · "Grooming"/To Do 176
// · "Ready for Test"/In Progress 138 · "merged to master"/Done 67 (all 67 with a Resolved date).
const jiraCSVWithStatusCategory = "Summary,Description,Status,Priority,Status Category,Resolved\n" +
	"Triaged in a custom workflow,d,New,High,To Do,\n" +
	"Groomed but not started,d,Grooming,High,To Do,\n" +
	"Under test,d,Ready for Test,High,In Progress,\n" +
	"Merged and shipped,d,merged to master,High,Done,23/Jul/2026 7:36 PM\n"

// THE DEFECT. Four statuses no Track vocabulary knows, each sitting next to Jira's own canonical
// answer in the same row of the same file. Today all four import as backlog.
func TestJiraCSVStatusCategory_ResolvesAnUnknownStatusFromTheColumn(t *testing.T) {
	got := mappedByTitle(t, jiraCSVWithStatusCategory)
	for title, want := range map[string]model.IssueStatus{
		"Triaged in a custom workflow": model.StatusTodo,
		"Groomed but not started":      model.StatusTodo,
		"Under test":                   model.StatusInProgress,
		"Merged and shipped":           model.StatusDone,
	} {
		i, ok := got[title]
		if !ok {
			t.Fatalf("%q did not import at all", title)
		}
		if i.Status != want {
			t.Errorf("%q: status = %q, want %q — the export's own Status Category column says so",
				title, i.Status, want)
		}
	}
}

// THE SECOND LOSS, and the one that reaches analytics: the Resolved date was refused because the
// gate reads the status this merge fixes. 129 of the corpus's 130 Done-category rows carry one.
func TestJiraCSVStatusCategory_ADoneCategoryAlsoLandsTheResolvedDate(t *testing.T) {
	got := mappedByTitle(t, jiraCSVWithStatusCategory)
	if c := got["Merged and shipped"].CompletedAt; c == nil {
		t.Errorf("CompletedAt = nil, want the Resolved date — the completion gate reads the status the category just resolved")
	}
	// The other three are NOT done and must not acquire one.
	for _, title := range []string{"Triaged in a custom workflow", "Groomed but not started", "Under test"} {
		if c := got[title].CompletedAt; c != nil {
			t.Errorf("%q: CompletedAt = %v, want nil — only a done row carries one", title, c)
		}
	}
}

// A resolution that CHANGED the status is reported, naming the path — the rule every Via field in
// this package exists for. The sentence is the one jira.go already ships; the field is the same
// field, spelled "Status Category" in a CSV and "statusCategory" in the API, so the vocabulary is
// deliberately NOT forked.
func TestJiraCSVStatusCategory_TheResolutionIsReported(t *testing.T) {
	ws := warningsFor(t, jiraCSVWithStatusCategory)
	for _, want := range []string{
		`unrecognised status "New" on 1 issue(s) — resolved via statusCategory "To Do" as "todo"`,
		`unrecognised status "merged to master" on 1 issue(s) — resolved via statusCategory "Done" as "done"`,
	} {
		if !hasLineContaining(ws, want) {
			t.Errorf("no warning reads %q; warnings = %#v", want, ws)
		}
	}
}

// THE NAME STILL WINS. 373 corpus rows have a recognised name whose category disagrees, and 34 of
// them are abandoned work filed under category Done — category-first imports those as delivered.
// GREEN BEFORE THIS MERGE AND AFTER IT: this is the over-reach control, not a new behaviour.
func TestJiraCSVStatusCategory_ARecognisedNameIsNotOverruledByTheCategory(t *testing.T) {
	got := mappedByTitle(t, "Summary,Description,Status,Priority,Status Category,Resolved\n"+
		"Abandoned in a Done category,d,Won't Do,High,Done,23/Jul/2026 7:36 PM\n"+
		"Backlogged in a To Do category,d,Backlog,High,To Do,\n")
	if s := got["Abandoned in a Done category"].Status; s != model.StatusCancelled {
		t.Errorf("status = %q, want %q — a name Track recognises is never overruled by the category", s, model.StatusCancelled)
	}
	if c := got["Abandoned in a Done category"].CompletedAt; c != nil {
		t.Errorf("CompletedAt = %v, want nil — abandoned work is not delivered work", c)
	}
	if s := got["Backlogged in a To Do category"].Status; s != model.StatusBacklog {
		t.Errorf("status = %q, want %q — Jira's To Do category is coarser than Track's backlog/todo split", s, model.StatusBacklog)
	}
}

// THE ORDERING, which is jira.go's and is load-bearing for the same reason there: the resolution
// runs AFTER the category, so a row that reached `done` only through the category is still
// overturned to cancelled by a Resolution that says the work was abandoned — and then correctly
// carries no completion time.
//
// ⚠ THE LIVE ASSERTION IS THE STATUS ONE. CompletedAt is nil TODAY as well, for an entirely
// different reason (the row imports as backlog), so a test asserting only that would pass before
// the merge and prove nothing.
func TestJiraCSVStatusCategory_TheResolutionStillOverturnsACategoryResolvedDone(t *testing.T) {
	got := mappedByTitle(t, "Summary,Description,Status,Priority,Status Category,Resolution,Resolved\n"+
		"Category says done but Jira says abandoned,d,merged to master,High,Done,Won't Do,23/Jul/2026 7:36 PM\n")
	i := got["Category says done but Jira says abandoned"]
	if i.Status != model.StatusCancelled {
		t.Errorf("status = %q, want %q — the Resolution runs after the category, exactly as on the API transport",
			i.Status, model.StatusCancelled)
	}
	if i.CompletedAt != nil {
		t.Errorf("CompletedAt = %v, want nil", i.CompletedAt)
	}
}

// THE TRAP, PINNED FROM BOTH SIDES. The API's key vocabulary is exactly the four keys #73 measured
// off /rest/api/2/statuscategory, and widening it with display names would make it accept values
// that transport can never send. The CSV column's display names are read by the CSV path instead —
// so this test says the two vocabularies are different AND that both reach an answer.
func TestJiraCSVStatusCategory_TheDisplayNameIsNotTheAPIKey(t *testing.T) {
	for _, display := range []string{"To Do", "In Progress"} {
		if got, ok := mapJiraStatusCategory(display); ok {
			t.Errorf("mapJiraStatusCategory(%q) = (%s, true); the API sends KEYS, and widening that "+
				"function is the fix that misses 1,294 of the corpus's 1,424 resolvable rows", display, got)
		}
	}
	// The key spellings occur in the CSV column too (2 corpus rows say `new`), so the CSV path must
	// read both. Asserted through the unchanged caller, not through a new signature.
	got := mappedByTitle(t, "Summary,Description,Status,Priority,Status Category\n"+
		"Key spelling,d,Needs Triage,High,new\n"+
		"Display spelling,d,Needs Triage,High,To Do\n")
	for _, title := range []string{"Key spelling", "Display spelling"} {
		if s := got[title].Status; s != model.StatusTodo {
			t.Errorf("%q: status = %q, want %q — both spellings of Jira's first category occur in real exports",
				title, s, model.StatusTodo)
		}
	}
}

// JIRA'S FOURTH CATEGORY IS NOT AN ANSWER. Key `undefined`, displayed "No Category", is Jira saying
// it does not know either; giving it a Track status invents exactly the meaning #73 refused to
// invent. It must fall through to backlog AND be reported as arrived-and-unusable — the corpus
// contains zero of these, so without this test the refusal is pinned by nothing.
//
// ⚠ ADDED AFTER THE FIRST RED-FIRST RUN, when C7 of the control harness showed that mapping
// "No Category" to a status was caught by NOTHING. Its red-first evidence is C1 (revert the fix),
// under which it fails on the wording.
func TestJiraCSVStatusCategory_NoCategoryIsRefusedNotInvented(t *testing.T) {
	body := "Summary,Description,Status,Priority,Status Category\nOne,d,Deployed,High,No Category\n"
	if s := mappedByTitle(t, body)["One"].Status; s != model.StatusBacklog {
		t.Errorf("status = %q, want %q — Jira's own \"No Category\" is not a Track status", s, model.StatusBacklog)
	}
	want := `unrecognised status "Deployed" on 1 issue(s) — statusCategory "No Category" carries no Track status, imported as "backlog"`
	if ws := warningsFor(t, body); !hasLineContaining(ws, want) {
		t.Errorf("no warning reads %q; warnings = %#v", want, ws)
	}
}

// AN EXPORT WITH NO SUCH COLUMN IS UNTOUCHED — 76 of the corpus's 304 files, and the case the four
// false sentences above were true of. Its warning must stay byte-identical: "no statusCategory
// present" here would be a sentence about a field that really was never in play.
// GREEN BEFORE AND AFTER.
func TestJiraCSVStatusCategory_AnExportWithoutTheColumnIsUnchanged(t *testing.T) {
	// The Created/Updated columns are present so this export produces EXACTLY ONE warning — an
	// assertion on the whole list, not on one member of it, so a new sentence about the missing
	// category column cannot hide behind a `contains`.
	ws := warningsFor(t, "Summary,Description,Status,Priority,Created,Updated\n"+
		"One,d,Deployed,High,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n")
	want := `unrecognised status "Deployed" on 1 issue(s) — imported as "backlog"`
	if len(ws) != 1 || ws[0] != want {
		t.Fatalf("warnings changed for a column-less export:\n got %q\nwant %q", strings.Join(ws, "|"), want)
	}
}

// A column that IS there and a cell that is EMPTY is a different fact from a column that is not
// there, and it is the structural-zero defence #73 established: without this line an operator
// cannot tell "this code never ran" from "your export had nothing to read".
func TestJiraCSVStatusCategory_APresentColumnWithABlankCellSaysSo(t *testing.T) {
	ws := warningsFor(t, "Summary,Description,Status,Priority,Status Category\nOne,d,Deployed,High,\n")
	want := `unrecognised status "Deployed" on 1 issue(s) — no statusCategory present, imported as "backlog"`
	if !hasLineContaining(ws, want) {
		t.Errorf("no warning reads %q; warnings = %#v", want, ws)
	}
}

// SCOPE. Linear's export has no such column and Linear's canonical field is state.type; a Linear CSV
// that happens to carry one must not be read through Jira's vocabulary. GREEN BEFORE AND AFTER —
// the control against wiring this into the shared mapper instead of the Jira one.
func TestJiraCSVStatusCategory_TheLinearCSVPathDoesNotReadIt(t *testing.T) {
	imp, store := newTestImporter()
	if _, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(
		"ID,Title,Description,Status,Priority,Status Category\nENG-1,One,d,Shipped it,High,Done\n")); err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d issues, want 1", len(store.created))
	}
	if s := store.created[0].Status; s != model.StatusBacklog {
		t.Errorf("status = %q, want %q — a Jira column name carries no meaning on the Linear transport", s, model.StatusBacklog)
	}
}
