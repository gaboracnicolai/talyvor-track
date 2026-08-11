package importer

// csv_unread_refs.go — THE ONE LOSS CLASS THIS IMPORTER NEVER REPORTED: a column the export
// populates that no mapper reads.
//
// ⚠⚠ THE FINDING, MEASURED WITH THE SHIPPED MAPPERS OVER THE WHOLE REAL CORPUS BEFORE A LINE OF
// THIS FILE EXISTED. Every note in this package is about a value that ARRIVED and could not be
// placed — an unrecognised status, a date in an unpinned shape, a column the export omitted. A
// value that arrives in a column the mapper simply does not look at produces nothing at all: the
// row imports, the job says `succeeded imported=N`, and the warnings list is empty. Whole
// population, both corpora, cells that are non-empty AND land nowhere AND are named by no note:
//
//	Linear   45 exports · 3,099 rows      Project 2,491 (80.4%) · Assignee 1,363 (44.0%)
//	                                      Parent issue 808 (26.1%) · Cycle Name 647 (20.9%)
//	Jira    302 exports · 18,807 rows     Assignee 9,960 of 17,768 rows-with-column (56.1%, 296 files)
//	                                      Sprint 7,575 of 11,259 (67.3%, 193 files)
//	                                      Parent 6,641 of 8,786 (75.6%, 119 files)
//
// That is larger than every reported class in this package combined, and it was silent.
//
// ⚠ THE FOUR ARE NOT A TASTE LIST. They are exactly the cross-object references model.Issue
// declares and issue.Store guards one by one with assertRefInWorkspace — project_id, cycle_id,
// assignee_id, parent_id. Track's own object graph defines the set; this file does not invent it.
// A fifth entry would be a claim about a Track column that does not exist, and
// TestUnreadRefTables_EveryColumnAppearsInARealExportHeader fails on one.
//
// ⚠ IT REPORTS, IT DOES NOT MAP, AND THE DIFFERENCE IS NOT LAZINESS. Every one of the four needs a
// JOIN KEY Track does not have and a policy nobody has chosen: a Linear `Assignee` cell is an
// EMAIL, the importer has no member store wired at all, and what happens when no member matches
// (skip / import unassigned + note / create the member) is a product decision with three defensible
// answers. Guessing one on an import path is how a silent loss becomes a wrong write. What needs no
// decision at all is telling the operator the value did not arrive — which is what this package
// already does for every other loss, and what it did not do for the biggest one.
//
// ⚠ CSV ONLY, DELIBERATELY, AND THE ASYMMETRY IS THE HONEST ONE. Neither API transport REQUESTS
// these fields (`jiraFields` and `linearIssuesQuery` name neither), so an API import cannot count
// how many issues carried an assignee — it never asked. "N issue(s) carried an assignee" would be a
// sentence that transport has no evidence for. The per-import statement it COULD make ("this
// importer does not read assignee at all") is a different sentence in a different place and is
// written up in the queue, not smuggled in here under the same constant.

// viaColumnNotRead is the path: the export CARRIED the column, the cell was POPULATED, and no
// mapper reads it. Distinct from every viaNo*Column constant, which is the opposite condition —
// the export did not carry the column at all.
const viaColumnNotRead = "column-not-read"

// The four Track references, named as the operator's Track vocabulary rather than as a db column:
// a warning that said `assignee_id` sends someone to a schema, and a warning that said `assignee`
// sends them to the issue.
const (
	fieldAssigneeRef = "assignee"
	fieldProjectRef  = "project"
	fieldCycleRef    = "cycle"
	fieldParentRef   = "parent issue"
)

// unreadRef pairs ONE provider column with the ONE Track reference its value would have filled.
// The column is carried into the note's Value so the rendered line names the export's own
// spelling — an operator told "cycle" cannot find that word anywhere in a Linear export, whose
// column is `Cycle Name`.
type unreadRef struct {
	column string
	field  string
}

// linearUnreadRefs — all four, and all 45 real exports carry all four columns.
//
// `Cycle Name` rather than `Cycle Number`: both are populated on exactly the same 647 rows, and the
// name is the half an operator can recognise. `Project` rather than `Project ID` for the same
// reason (2,491 rows vs 2,448 — the ID is absent on 43 rows that do carry a project name).
var linearUnreadRefs = []unreadRef{
	{"Assignee", fieldAssigneeRef},
	{"Project", fieldProjectRef},
	{"Cycle Name", fieldCycleRef},
	{"Parent issue", fieldParentRef},
}

// jiraUnreadRefs — THREE, not four, and the missing one is the measurement.
//
// ⚠ JIRA'S `Project` IS NOT A LOST TRACK PROJECT. A Jira project is the container this import maps
// to a Track TEAM — the operator supplies team_id on the job row and every imported issue lands in
// it. Reporting `Project key` (16,020 of 16,284 rows, 98.4%) as a dropped project_id would be the
// largest number in this file and the only false one.
var jiraUnreadRefs = []unreadRef{
	{"Assignee", fieldAssigneeRef},
	{"Sprint", fieldCycleRef},
	{"Parent", fieldParentRef},
}

// unreadRefNotes reports one note per POPULATED reference column on this row.
//
// The gate is the CELL, not the header, and that is what keeps the report worth reading: an export
// that carries an `Assignee` column and no assignees has lost nothing, and a warning on every such
// import is one an operator learns to skip. An ABSENT column is silent here too — that is a real
// condition with its own vocabulary in this package (viaNo*Column), and it is not "a value arrived
// and was dropped".
func unreadRefNotes(ci columnIndex, row []string, refs []unreadRef) []FieldNote {
	var out []FieldNote
	for _, r := range refs {
		if !ci.has(r.column) {
			continue
		}
		if ci.get(row, r.column) == "" {
			continue
		}
		// Value is the COLUMN NAME, never the cell. The tally in run() keys on the whole FieldNote,
		// so putting a cell value here would turn a 10,000-row import into 10,000 distinct notes —
		// the exact failure #80's bound exists to prevent — and would spill assignee identities
		// into a job row that is read by anyone who can read the job.
		out = append(out, FieldNote{Field: r.field, Value: r.column, Via: viaColumnNotRead})
	}
	return out
}
