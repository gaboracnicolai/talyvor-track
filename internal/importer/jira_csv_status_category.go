package importer

import (
	"strings"

	"github.com/talyvor/track/internal/model"
)

// jira_csv_status_category.go — Jira's CANONICAL, non-renameable state, in the column this package
// said four times a CSV export does not have.
//
// ⚠⚠ THE PREMISE WAS WRITTEN DOWN FOUR TIMES AND NEVER MEASURED. Before this file, all four of
// these sentences were in the repository, and all four were false:
//
//	csv.go, FieldNote.Via      `Via "" (no fallback exists for this transport — every CSV path)`
//	csv.go, statusFallback     "a Jira CSV export carries no category column"
//	jira.go, mapJiraIssues     "a route the CSV mapper does not have"
//	status_category_test.go    "a Jira CSV export carries no such column"
//
// MEASURED WHOLE-POPULATION over #103's 304-file corpus of real Jira CSV exports committed to public
// repositories by unrelated instances, read as raw bytes, classified by this package's own mappers:
//
//	exports carrying a "Status Category" column                 228 of 304   (75.0%)
//	data rows                                                       17,657
//	  status NAME unrecognised ⇒ imported as backlog                 3,167   (17.9%)
//	     with a Status Category cell saying what it is               1,424   ← what this file reads
//	     with no such cell                                           1,743
//	     with a cell this file still cannot place                        0
//	files with ≥1 resolvable row  57 of 304 · ≥50% of their rows 23 · 100% of their rows 5
//
// ⚠⚠ THE COLUMN CARRIES THE DISPLAY NAME, NOT THE API KEY, AND THAT IS WHY THIS IS NOT A ONE-LINE
// REUSE OF mapJiraStatusCategory. That function reads the four category KEYS #73 measured off
// /rest/api/2/statuscategory (`new` · `indeterminate` · `done` · `undefined`); the CSV column spells
// the same four as the names a human sees. Passing this column to it resolves 130 of the 1,424 rows
// and silently misses 1,294 — "Done" collides with its own key, "To Do" and "In Progress" do not.
// The corpus's entire category vocabulary is `To Do` 5,831 · `Done` 4,037 · `In Progress` 1,414 ·
// `new` 2 — the key spellings occur in real exports too, which is why both are read and why the two
// display-only spellings are added HERE rather than to the API function, whose transport can never
// send them.
//
// ⚠ IT IS THE SECOND CHANCE, NEVER THE FIRST. mapJiraStatus runs first and is never overruled: 373
// corpus rows carry a recognised name whose category disagrees with it, and the disagreement runs
// the dangerous way — `Won't Do` and `Canceled` (34 rows) are filed under category `Done`, so a
// category-first mapper imports abandoned work as delivered. Same order as jira.go, for the same
// reason, and TestJiraCSVStatusCategory_ARecognisedNameIsNotOverruledByTheCategory pins it.
//
// ⚠ TWO LOSSES, ONE CAUSE. jiraCSVResolved gates the completion time on the row importing as `done`,
// so a Done-category row that imported as backlog had its Resolved date refused as well — 129 of the
// corpus's 130 Done-category rows carry one. Reading the category lands the status AND the date.
//
// ⚠ THE WARNING SAYS `statusCategory`, WHICH IS THE API'S SPELLING, ON PURPOSE. It is one field of
// one provider, spelled `Status Category` in an export and `statusCategory` in the REST response;
// forking the sentence would tell a Jira operator there are two things to go and look at. This is
// the opposite case from Linear's state.type, which is a DIFFERENT field of a DIFFERENT provider and
// keeps its own constants for exactly that reason.
const jiraCSVStatusCategoryColumn = "Status Category"

// mapJiraCSVStatusCategory places the CSV column's value. It adds the two display spellings that
// differ from their key and DELEGATES the rest, so the two transports cannot drift apart on what a
// Jira category means — only on how it is spelled.
func mapJiraCSVStatusCategory(v string) (model.IssueStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "to do":
		return model.StatusTodo, true
	case "in progress":
		return model.StatusInProgress, true
	}
	// `Done` is spelled identically to its key, and `new` occurs verbatim in the corpus. Jira's
	// fourth category — key `undefined`, displayed "No Category" — is refused by that function on
	// purpose (#73: Jira saying it does not know either), and falls through to being REPORTED.
	return mapJiraStatusCategory(v)
}

// jiraCSVStatusCategory is the CSV twin of resolveJiraStatusCategory, and it takes the whole
// columnIndex for the reason jiraCSVCreated does: ci.get answers "" for a missing HEADER and for an
// empty CELL alike, and those two are not the same statement.
//
//	no such column   ⇒ zero statusFallback, so the warning keeps the wording it has had since #72.
//	                   76 of the corpus's 304 files, and the only case the four sentences above
//	                   were ever true of. Claiming "no statusCategory present" there would be a
//	                   sentence about a field that really was never in play.
//	column, no value ⇒ viaNoCategory — the export HAS the column and this row is blank.
//	value, unplaced  ⇒ viaCategory, unresolved. Nothing invented, the value named.
//	value, placed    ⇒ viaCategory, resolved, naming the value that decided it.
//
// Those last three are the same three resolveJiraStatusCategory reports, deliberately: an operator
// comparing a CSV import with an API import of the same project reads one vocabulary.
func jiraCSVStatusCategory(ci columnIndex, row []string, unresolved model.IssueStatus) (model.IssueStatus, statusFallback) {
	if len(ci[strings.ToLower(jiraCSVStatusCategoryColumn)]) == 0 {
		return unresolved, statusFallback{}
	}
	raw := ci.get(row, jiraCSVStatusCategoryColumn)
	if raw == "" {
		return unresolved, statusFallback{via: viaNoCategory}
	}
	mapped, ok := mapJiraCSVStatusCategory(raw)
	if !ok {
		return unresolved, statusFallback{via: viaCategory, value: raw}
	}
	return mapped, statusFallback{via: viaCategory, value: raw, resolved: true}
}
