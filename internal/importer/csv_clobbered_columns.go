package importer

// csv_clobbered_columns.go — the columns a CSV re-import DELETES, and the report that says so.
//
// ⚠⚠ THE CONDITION IS AN ABSENT COLUMN, NOT AN EMPTY CELL, AND `columnIndex.get` CANNOT TELL THEM
// APART. It answers "" for both, by design and by its own doc comment. That is right for a mapper
// deciding what to write into a NEW row and wrong for the one statement that overwrites an existing
// one: issue.Store.UpsertByIdentifier's conflict arm CLOBBERS `description` and `labels` from
// EXCLUDED under the argument "provider is source of truth". A column the export does not carry is
// not the provider saying "empty" — it is the export not being asked for the field.
//
// MEASURED end to end on real Postgres through the shipped runner (csv_clobbered_columns_job_test.go):
// an "all fields" export followed by a "current fields" export of the SAME issue left
// description="" labels=[] and reported `succeeded imported=1 skipped=0 failed=0`.
//
// WHOLE-POPULATION over #103's 305 cached real Jira exports, file selection spelled the way
// buildIndex spells it (lowercased, BOM stripped as a file prefix):
//
//	no `Labels` column        203 of 305   (66.6%)
//	no `Description` column    16 of 305   ( 5.2%)
//	neither                    15 of 305
//
// Re-run BOTH halves — scripts/w34-csv-clobbered-columns-probe.py extracts the headers and decides
// nothing; TestJiraCSVCorpus_ClobberedColumnPresence asks THIS FILE's `has` which of them carry the
// columns and pins the two figures. They skip without the corpus and say so.
//
// ⚠ 305, NOT #104's 304. That census selects Jira exports on a case-SENSITIVE `Issue key`; buildIndex
// lowercases, so the product also reads a file headed `Issue Key`. Both numbers are true of their own
// question and the difference is one six-column event log with no `Summary`, every row of which
// errEmptyTitle refuses either way.
//
// ⚠ THIS FILE REPORTS. IT DOES NOT DECIDE. Whether an absent column should PRESERVE the stored
// value is a rule with three defensible answers and no way to express the middle one today:
// model.Issue.Description is a `string`, so the mapper has no value that means "no statement", and
// the store's existing idiom for that (a nullable parameter + COALESCE, as `created_at` and
// `updated_at` already use) needs something model.Issue does not carry. Guessing it here would trade
// a silent deletion for a silent refusal-to-clear, which is the same class of defect pointing the
// other way. It is written up in the queue with these numbers.
//
// ⚠ THE GATE IS THE WRITE, NOT THE HEADER. A first import of a narrow export overwrites nothing, so
// a header-only warning would fire on every first import and be tuned out inside a week. The gate is
// the `inserted` boolean UpsertByIdentifier has returned since #71 and run() discarded until now.

// The two columns the conflict arm clobbers that a CSV row can fail to speak about. Spelled exactly
// as BOTH csv mappers read them — jiraRowMapper and linearRowMapper use the same two literals, so
// unlike the Created/Updated pairs there is no per-provider spelling to keep apart.
//
// ⚠ `title` IS THE THIRD CLOBBERED COLUMN AND IS DELIBERATELY NOT HERE: a row with no title is
// REFUSED by errEmptyTitle and never reaches the write path at all, so its absence cannot delete
// anything. Listing it would produce a warning no import can ever emit.
const (
	clobberedDescriptionColumn = "Description"
	clobberedLabelsColumn      = "Labels"
)

const (
	viaNoDescriptionColumn = "no-Description-column"
	viaNoLabelsColumn      = "no-Labels-column"
)

// csvClobberedColumnNotes returns one note per CLOBBERED column this export does not carry.
//
// They are returned SEPARATELY from a mapper's ordinary notes and are not reported by themselves:
// run() folds them in only for a row that UPDATED an issue that already existed. See mappedIssue.
//
// ⚠ IT TAKES THE HEADER AND NOT THE ROW, WHICH IS WHAT BOUNDS THE REPORT. Every row of an export
// produces the IDENTICAL FieldNote, so renderWarnings groups a 10,000-row re-import into ONE line
// per column carrying the count — the bound #80 built, reached here by construction rather than by
// remembering to apply it. A per-row value is not merely discouraged in this function, it is
// unreachable: control C11 has to be applied at the CALL SITE to express it at all.
func csvClobberedColumnNotes(ci columnIndex) []FieldNote {
	var out []FieldNote
	if !ci.has(clobberedDescriptionColumn) {
		out = append(out, FieldNote{Field: "description", Via: viaNoDescriptionColumn})
	}
	if !ci.has(clobberedLabelsColumn) {
		out = append(out, FieldNote{Field: "labels", Via: viaNoLabelsColumn})
	}
	return out
}
