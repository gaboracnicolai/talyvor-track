package importer

import (
	"sort"
	"strings"
)

// csv_custom_fields.go — THE CLASS csv_dropped_objects.go OPENED, APPLIED TO THE TRACK OBJECT WITH
// THE WIDEST POPULATION IN THE CORPUS AND NO ENTRY OF ITS OWN.
//
// ⚠⚠ MEASURED WHOLE-POPULATION over the 346 cached real Jira exports, before a line of this file
// existed (csv_custom_fields_corpus_census_test.go):
//
//	13,255 of 18,807 rows (70.5%) in 282 of 302 exports carry at least one POPULATED
//	`Custom field (…)` column that no entry in this package reads or reports, across 345
//	DISTINCT spellings.
//
// The population is the 302 GENUINE Jira exports of the 346 cached files — a header carrying both
// `Summary` and `Status` — which is the same denominator csv_dropped_objects.go's 18,807 uses. The
// other 44 are hand-built spreadsheets; counting them would inflate the file count and dilute the
// rate, and csv_unread_refs.go already had to separate them once.
//
// Every one of those rows imported with `succeeded imported=N` and an empty warnings list.
//
// ⚠ THE FINDING DOES NOT REST ON ONE NOISY COLUMN, AND THAT IS MEASURED RATHER THAN ASSERTED.
// `Custom field (Rank)` — Jira's internal LexoRank ordering token — is the single largest
// contributor at 12,617 rows. Excluding it the figure is still 11,991 rows in 266 exports over 344
// spellings; excluding Rank, Story Points and Start Date together it is 10,678 in 255 exports over
// 342. So Rank is reported like the rest: the sentence this file renders is "this importer
// does not read it", which is true of Rank, and inventing a taste list of which of a tenant's own
// fields are worth mentioning is exactly the judgement an operator is better placed to make.
//
// ⚠ THE TEST FOR "IS THIS REPORTABLE" IS WHETHER TRACK SHIPS THE OBJECT — the rule
// csv_unread_refs.go applies to keep `Estimate` out, and the rule that let `comment` in. Track
// ships custom fields further than it ships either of csv_dropped_objects.go's two:
//
//   - `custom_fields` + `issue_field_values` — migration 0010, internal/customfield's store and
//     handler, SIX mounted routes (GET/POST /workspaces/{wsID}/custom-fields, PATCH/DELETE on one,
//     GET /issues/{id}/fields, PUT /issues/{id}/fields/{fieldID}), and model.Issue.FieldValues,
//     which the issue read paths populate via WithFieldFetcher.
//   - ⚠ AND A FRONTEND THAT CALLS THEM — frontend/src/api/customFields.ts and
//     frontend/src/hooks/useCustomFields.ts. `comments` was admitted to the report with ZERO
//     frontend callers on the strength of the API alone; this object has both ends.
//
// ⚠ IT REPORTS, IT DOES NOT MAP, and the gap here is wider than a join key. An imported value needs
// a `custom_fields` DEFINITION to hang on — a name, a type, and for a select field its option list
// — and a Jira CSV carries the field's NAME and nothing else: no type, no options, no id. Creating
// 345 definitions per workspace from column headers is a product decision with a schema in it, not
// a patch, and it is the same unresolved shape as the four unread references.
//
// ⚠ THE STORY-POINTS RECONCILIATION, SAID RATHER THAN LEFT TO LOOK LIKE A CONTRADICTION.
// csv_dropped_objects.go keeps `Story Points` silent because "a warning about them names a
// capability that does not exist" — model.Issue has no estimate field, and that is still true.
// `Custom field (Story Points)` is reported HERE under a DIFFERENT capability, one that does exist:
// not "Track lost your estimate" but "Track has a custom-field object and this import created no
// value in it". The two sentences do not collide because neither claims Track has an estimate.
//
// ⚠ JIRA ONLY, AND MEASURED: zero of the 46 cached real Linear exports carry a column starting
// `Custom field (`. A Linear table here could only fire on a fabricated header — the same argument
// csv_dropped_objects.go makes for comments and logged time.

// fieldCustomFieldObj is the Track object, named as an operator's Track vocabulary rather than as a
// table: a warning that said `issue_field_values` sends someone to a schema.
const fieldCustomFieldObj = "custom field value"

// viaCustomFieldNotCreated is a SEPARATE PATH FROM viaObjectNotCreated, and sharing that one was a
// measured mistake rather than a style choice.
//
// ⚠⚠ csv_dropped_objects_test.go's TestDroppedObjects_AValueWithNoTrackObjectIsNotReported SCOPES
// ITSELF BY `Via == viaObjectNotCreated` ALONE, and its example of "a value with no Track object"
// is `Custom field (Story Points)` — the exact cell this entry reports. Reusing the via turned that
// neighbouring guard RED on the full `go test ./...`, and the two ways out were not equal: widening
// the neighbour's scope would have edited a guard to accommodate this change, which is how a report
// quietly loses its meaning. A separate via leaves that file byte-identical and lets this sentence
// say the thing that is actually true here.
//
// ⚠ AND THE SENTENCES REALLY ARE DIFFERENT. A comment fails to become ONE row in a table that
// already exists. A custom-field value has no table row AND no `custom_fields` DEFINITION to hang
// on — the Jira CSV supplies the field's name and nothing else, no type and no option list — so
// "no Track X is created from it" would understate it by half. The same argument
// viaIssueLinkNotRead makes for being separate from viaColumnNotRead.
const viaCustomFieldNotCreated = "custom-field-not-created"

// jiraCustomFieldPrefix is Jira's own. Its CSV export prefixes EVERY custom field this way —
// csv_unread_refs.go rests on the same fact when it distinguishes `Custom field (Epic Link)` (a
// real Jira export) from a bare `Epic Link` (a hand-built spreadsheet, 5 files in this corpus).
//
// ⚠ A PREFIX RULE, NOT A CONTAINS RULE, AND THE DIRECTION MATTERS. csv_dropped_objects.go lists
// EXACT spellings because `Custom field (Time Spent)` exists here on 20 rows and holds a DATE
// rather than logged work. That is the same fact seen from the other side: the parenthesised
// spelling is a custom field and belongs to this entry, the bare one is logged work and belongs to
// that one, and only a prefix rule keeps them apart.
const jiraCustomFieldPrefix = "custom field ("

// jiraCustomFieldsReportedElsewhere holds every custom-field spelling ANOTHER entry in this package
// already claims, so one dropped value never produces two sentences in two vocabularies.
//
// ⚠ TODAY IT IS EXACTLY ONE, AND IT IS NOT HAND-COPIED FOLKLORE:
// TestCustomFields_TheExclusionListIsExactlyWhatAnotherEntryClaims derives the set from
// jiraUnreadRefs and jiraObjectColumns and fails the day a table starts claiming a second
// custom-field spelling without this list following it.
//
// `Custom field (Epic Link)` is populated on 3,630 rows of this corpus and is reported as a parent
// reference (csv_unread_refs.go). Reporting it again as an uncreated custom field would tell an
// operator two different things about one dropped link.
//
// ⚠ ITS PARENT NOTE FIRES ON 1,748 OF THOSE 3,630 AND THAT IS NOT A GAP THIS EXCLUSION OPENED —
// unreadRefNotes emits ONE note per entry and `Parent` wins where both spellings are populated,
// which is the 1,748/1,766 split csv_unread_refs.go already measured. The census asserts both
// directions so the exclusion cannot silently become a silencing.
var jiraCustomFieldsReportedElsewhere = map[string]bool{
	"custom field (epic link)": true,
}

// jiraCustomFieldSpellings returns the custom-field columns THIS export's header carries,
// lowercased and sorted, minus the ones another entry reports.
//
// ⚠ DISCOVERED FROM THE HEADER RATHER THAN LISTED, for the reason jiraIssueLinkSpellings is: a
// rowMapper never sees the header, columnIndex is the only place a spelling can come from, and the
// names are the tenant's own — 345 distinct ones in 302 exports, which is not a list anybody
// maintains by hand.
//
// ⚠ SORTED because it is derived from columnIndex, whose iteration order is a map's: an unsorted
// result would make two imports of one file emit their notes in different orders, and the warnings
// a job row carries are read by diffing them.
func jiraCustomFieldSpellings(ci columnIndex) []string {
	var out []string
	for column := range ci {
		if !strings.HasPrefix(column, jiraCustomFieldPrefix) {
			continue
		}
		if jiraCustomFieldsReportedElsewhere[column] {
			continue
		}
		out = append(out, column)
	}
	sort.Strings(out)
	return out
}

// customFieldNotes reports ONE note per custom-field spelling this row populates.
//
// ⚠ ONE NOTE PER SPELLING, NOT ONE PER ROW — issueLinkNotes' rule rather than droppedObjectNotes'.
// Two spellings of the PARENT are one dropped reference and get one note; `Custom field (Severity)`
// and `Custom field (Test level)` are two different fields holding two different values, and
// collapsing them would tell an operator one field was lost when four were. The bound holds by
// construction: renderWarnings groups on (field, via) and shows at most maxWarningExemplars
// distinct values with one summary line for the rest, so 345 spellings cannot flood a report.
//
// ⚠⚠ THE GATE IS ANY OCCURRENCE, WHICH IS WHY THIS DOES NOT USE ci.get. A csv-all-fields export
// emits one column PER VALUE under the same name — `Custom field (External issue ID)` reaches 40
// occurrences in one real header here, `Custom field (Department)` 21 — and ci.get names the FIRST
// (csv.go:442), so a row whose first cell is empty and whose later one holds the value would
// produce nothing.
//
// ⚠ AND HERE THAT CHOICE BUYS REAL ROWS, WHICH IS WHERE THIS ENTRY DIFFERS FROM THE LINK ONE.
// csv_issue_links.go records that first-occurrence and any-occurrence agree on every row of this
// corpus, so its gate "buys 0 rows now" and is pinned by a literal rather than by a figure. For
// custom fields they DISAGREE: measured over the same 302 exports, a first-occurrence gate emits
// 56,568 notes against this gate's 57,740 — it would LOSE 1,172, across 10 exports and 20
// spellings, `Custom field (Test level)` (316), `(Test steps)` (247) and `(Expected result)` (246)
// worst. Control C2 flips the gate to ci.get and the corpus census reds on exactly those columns.
//
// The VALUE is the column name as columnIndex holds it — never the cell. A custom field holds
// whatever the instance put in it (a customer name, an approver, a free-text incident note), and
// the job row is readable by every member of the workspace. That is the stronger of the two reasons
// here; the note bound (#80) is the lesser one.
func customFieldNotes(ci columnIndex, row []string, spellings []string) []FieldNote {
	var out []FieldNote
	for _, column := range spellings {
		if len(ci.getAll(row, column)) == 0 {
			continue
		}
		out = append(out, FieldNote{
			Field: fieldCustomFieldObj,
			Value: column,
			Via:   viaCustomFieldNotCreated,
		})
	}
	return out
}
