package importer

// csv_dropped_objects.go — THE CLASS csv_issue_links.go OPENED, GENERALISED TO THE OTHER TRACK
// OBJECTS A REAL JIRA EXPORT CARRIES VALUES FOR AND THIS IMPORTER NEVER CREATES.
//
// ⚠⚠ MEASURED WHOLE-POPULATION over the 302 cached real Jira exports with the shipped mapper,
// before a line of this file existed (csv_dropped_objects_corpus_census_test.go):
//
//	Comment      2,317 of 18,807 rows (12.3%) in 163 of 302 exports — the column repeats once per
//	             comment, up to 69 times in one real header
//	Time Spent     283 of 18,807 rows ( 1.5%) — seconds of logged work, integer on every one of them
//
// Every one of those rows imported with `succeeded imported=N` and an empty warnings list.
//
// ⚠ THE TEST FOR "IS THIS REPORTABLE" IS WHETHER TRACK SHIPS THE OBJECT, AND IT IS THE ONE
// csv_unread_refs.go ALREADY APPLIES TO KEEP `Estimate` OUT. A dropped value is worth a line only
// when Track has somewhere for it to have gone:
//
//   - `comments` — a table, a store (internal/issue/comments.go), four mounted routes
//     (POST/GET/PATCH/DELETE /issues/{id}/comments) and its own tenancy locks. ⚠ AND NO FRONTEND
//     SURFACE CALLS THEM — measured, zero references in frontend/src, the same shape as the import
//     endpoints themselves. The API is where an imported comment would have been readable, and it
//     returns an empty list for every imported issue.
//   - `time_entries` — a table, a store, a mounted handler AND a rendered surface: TimeTracker,
//     TimeEntry and the TimeReport page all read it.
//   - `Original estimate` (848 rows), `Σ Original Estimate` (297), `Remaining Estimate` (385),
//     `Work Ratio` (307) and Story Points
//     have NO Track object at all — time_entries has no estimate column and model.Issue has no
//     estimate field — so they stay silent. A warning about them names a capability that does not
//     exist, which is the rule that keeps `Estimate` out of the unread-reference table.
//
// ⚠ THE Σ ROLL-UP IS EXCLUDED AND THAT IS A MEASUREMENT, NOT AN OVERSIGHT. `Σ Time Spent` is
// populated on 838 rows against `Time Spent`'s 283 and is Jira's sum over the issue AND its
// subtasks. Reporting it would tell an operator a parent lost work that was logged on its children
// and is already counted under them. Same reasoning as `Epic Link Summary` in csv_unread_refs.go: a
// column that is ABOUT the value is not the value.
//
// ⚠ IT REPORTS, IT DOES NOT MAP. A `comments` row needs author_id (NOT NULL) and a `time_entries`
// row needs member_id (NOT NULL) — the importer has no member store wired at all, and the Jira cell
// names a person by display name. That is the same join key and the same unresolved-member policy
// the four unread references are waiting on.
//
// ⚠ JIRA ONLY, AND MEASURED: no real Linear export carries a comment or a time column, and its
// `Estimate` is story points. A Linear table here could only fire on a fabricated header.

// The Track objects, named as the operator's Track vocabulary rather than as a table: a warning
// that said `time_entries` sends someone to a schema.
const (
	fieldCommentObj    = "comment"
	fieldLoggedTimeObj = "logged time"
)

// viaObjectNotCreated is the path: the export CARRIED the column, the cell was POPULATED, and the
// Track object that value belongs to is never created.
//
// ⚠ A SEPARATE CONSTANT FROM viaColumnNotRead FOR THE REASON viaIssueLinkNotRead IS ONE: the
// sentence that renders — "their Track X is left empty" — describes a nullable FIELD ON THE ISSUE.
// Nothing on an imported issue is left empty here; a row in another table is never written.
const viaObjectNotCreated = "object-not-created"

// objectColumn pairs the provider's spelling(s) of one column with the ONE Track object its value
// would have become. A list of spellings for the same reason unreadRef carries one.
type objectColumn struct {
	columns []string
	field   string
}

// jiraObjectColumns — EXACT spellings, deliberately not a prefix rule. Unlike the issue-link column
// (whose parenthesised link type is configured per instance), these two names are Jira's own and do
// not vary; `Custom field (Time Spent)` exists in this corpus on 20 rows and holds a DATE, so a
// contains-rule would report a column that is not logged work at all.
var jiraObjectColumns = []objectColumn{
	{[]string{"Comment"}, fieldCommentObj},
	{[]string{"Time Spent"}, fieldLoggedTimeObj},
}

// droppedObjectNotes reports ONE note per entry whose column this row populates.
//
// ⚠ THE GATE IS ANY OCCURRENCE, WHICH IS WHY THIS DOES NOT USE ci.get. Jira emits ONE `Comment`
// COLUMN PER COMMENT — 69 occurrences in one real header — and ci.get names the FIRST (csv.go:422).
// One dropped object class is one line per row however many cells carry it.
//
// The VALUE is the column name, never the cell: the note bound (#80) is the lesser reason here, and
// the greater one is that a `Comment` cell is another person's words and the job row is readable by
// every member of the workspace.
func droppedObjectNotes(ci columnIndex, row []string, objs []objectColumn) []FieldNote {
	var out []FieldNote
	for _, o := range objs {
		for _, col := range o.columns {
			if len(ci.getAll(row, col)) == 0 {
				continue
			}
			out = append(out, FieldNote{Field: o.field, Value: col, Via: viaObjectNotCreated})
			break
		}
	}
	return out
}
