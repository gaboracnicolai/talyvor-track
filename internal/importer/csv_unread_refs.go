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
//	                                      parent issue 8,407 rows across THREE spellings (see below)
//
// That is larger than every reported class in this package combined, and it was silent.
//
// ⚠ THE PARENT LINE READ "Parent 6,641 of 8,786 (75.6%, 119 files)" AND THAT NUMBER WAS THE COLUMN'S,
// NOT THE REFERENCE'S. Jira spells the parent link three ways and this table knew one, so the
// paragraph was an accurate count of a subset presented as the class: 2,455 further rows — 27.0% of
// every corpus row carrying a parent link — arrived with one and produced no note. The entry now
// reaches 8,407. Whoever edits a count in this comment: it is the entry that has a population, and a
// per-column figure quoted as the class is the shape of the defect this line used to hide.
//
// ⚠ THEY ARE NOT A TASTE LIST. They are the cross-object references model.Issue declares, and
// Track's own object graph defines the set; this file does not invent it. An entry naming a Track
// column that does not exist is caught by TestUnreadRefTables_EveryColumnAppearsInARealExportHeader.
//
// ⚠⚠ THAT SENTENCE USED TO READ "exactly the cross-object references model.Issue declares AND
// issue.Store guards one by one with assertRefInWorkspace — project_id, cycle_id, assignee_id,
// parent_id", AND ITS TWO HALVES NAME DIFFERENT SETS. model.Issue declares FIVE references;
// assertRefInWorkspace guards FOUR. The fifth — CreatorID — is missing from the guard list for a
// reason that has nothing to do with reporting: it is server-stamped, never client-supplied, so
// there is no client value to validate. Deriving the report's set from the GUARD's set left the
// fifth silent for four merges while 13,828 Jira and 3,056 Linear real rows carried one, and the
// half-true sentence is what made the omission read as a decision. See csv_creator_ref_test.go.
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

// viaColumnNotReadStamped is the SAME loss with a different consequence, and it is a second
// constant rather than a fifth row under the first because the sentence the first renders is FALSE
// of it. The four references above are nullable columns that end up NULL — "left empty" is exactly
// what happened. issues.creator_id is NOT NULL and run() stamps model.ImporterCreatorID on every
// row it writes, so the provider's value is REPLACED, not dropped, and every imported issue reads
// as filed by "importer". See csv_creator_ref_test.go for the population and for the assertion that
// refuses the shared sentence.
const viaColumnNotReadStamped = "column-not-read-stamped"

// The Track references, named as the operator's Track vocabulary rather than as a db column:
// a warning that said `assignee_id` sends someone to a schema, and a warning that said `assignee`
// sends them to the issue.
const (
	fieldAssigneeRef = "assignee"
	fieldProjectRef  = "project"
	fieldCycleRef    = "cycle"
	fieldParentRef   = "parent issue"
	// The fifth, and the one the sentence above was wrong about. model.Issue declares it
	// (CreatorID) and issue.Store does NOT guard it with assertRefInWorkspace, because it is
	// server-stamped rather than client-supplied — which is why a set derived from the guard list
	// missed it for four merges while 13,828 Jira and 3,056 Linear real rows carried one.
	fieldCreatorRef = "creator"
)

// unreadRef pairs the provider's SPELLINGS of one column with the ONE Track reference its value
// would have filled. The spelling that actually held the value is carried into the note's Value so
// the rendered line names the export's own word — an operator told "cycle" cannot find that anywhere
// in a Linear export, whose column is `Cycle Name`.
//
// ⚠⚠ `columns` IS A LIST BECAUSE THIS WAS A SINGLE STRING AND ONE PROVIDER SPELLS ONE FIELD MORE
// THAN ONE WAY. columnIndex matches a header EXACTLY (lower-cased, trimmed), so `{"Parent", …}`
// could only ever see a column called `Parent` — and 116 of the 302 real Jira exports carry a parent
// reference with NO such column, spelling it `Custom field (Epic Link)` instead. 2,455 corpus rows
// (27.0% of every row that carries a parent link) arrived with one, wrote parent_id NULL, and
// produced no warning. See csv_parent_spellings_test.go for the whole-population census, for the two
// spellings this list DOES NOT contain and the rows that decision costs, and for why a name-shaped
// column (`Epic Link Summary`) is not a reference.
//
// ⚠ ORDER IS PREFERENCE, AND IT IS LOAD-BEARING RATHER THAN COSMETIC. unreadRefNotes emits ONE note
// per entry per row and names the FIRST spelling that is both present and populated. 2,926 corpus
// rows populate two spellings of the parent at once, and a note per matching spelling would tell an
// operator two things about one dropped value. Keeping the previously-shipped spelling first is also
// what leaves this report's output byte-identical on all 6,641 rows it already covered.
//
// via names the sentence this entry renders through. It is per-entry rather than a constant in
// unreadRefNotes because the five references do not have one consequence: four end up NULL and one
// ends up stamped. An entry whose via the renderer does not know falls through to the generic
// "unrecognised <field> <value>" line, which is a true-looking sentence about a column that was
// never read — TestUnreadRefTables_EveryEntryNamesAViaTheRendererKnows refuses that.
type unreadRef struct {
	columns []string
	field   string
	via     string
}

// linearUnreadRefs — all four, and all 45 real exports carry all four columns.
//
// `Cycle Name` rather than `Cycle Number`: both are populated on exactly the same 647 rows, and the
// name is the half an operator can recognise. `Project` rather than `Project ID` for the same
// reason (2,491 rows vs 2,448 — the ID is absent on 43 rows that do carry a project name).
//
// ⚠ ONE SPELLING EACH, AND THAT IS THE MEASUREMENT RATHER THAN THE DEFAULT. All 45 real exports
// carry all four columns under exactly these names — Linear emits two published header shapes (30
// and 34 columns) and the reference spellings are identical in both, so there is no second spelling
// for this table to carry. The Jira half below is the provider that needed a list.
var linearUnreadRefs = []unreadRef{
	{[]string{"Assignee"}, fieldAssigneeRef, viaColumnNotRead},
	{[]string{"Project"}, fieldProjectRef, viaColumnNotRead},
	{[]string{"Cycle Name"}, fieldCycleRef, viaColumnNotRead},
	{[]string{"Parent issue"}, fieldParentRef, viaColumnNotRead},
	// The fifth. 3,056 of the 3,099 rows that carry the column (98.6%), in 45 of 45 real exports,
	// and the cell is an EMAIL — the same join key Track does not have that keeps `Assignee` a
	// report rather than a mapping. Linear has no Reporter column; its Jira twin has two.
	{[]string{"Creator"}, fieldCreatorRef, viaColumnNotReadStamped},
}

// jiraUnreadRefs — FOUR of the five references, across FIVE columns, and both counts are the
// measurement rather than the shape of the Linear table copied over.
//
// ⚠ JIRA'S `Project` IS NOT A LOST TRACK PROJECT. A Jira project is the container this import maps
// to a Track TEAM — the operator supplies team_id on the job row and every imported issue lands in
// it. Reporting `Project key` (16,020 of 16,284 rows, 98.4%) as a dropped project_id would be the
// largest number in this file and the only false one.
//
// ⚠ TWO COLUMNS FOR ONE TRACK FIELD, AND NAMING BOTH IS WHAT AVOIDS A DECISION RATHER THAN MAKING
// ONE. Jira's `Creator` (who created the issue, immutable) and `Reporter` (who it is filed on
// behalf of) are DIFFERENT people on 1,187 of the 13,797 corpus rows that populate both (8.6%), so
// neither line stands for the other; which one should become Track's creator_id is a product
// decision with two defensible answers. Populations: Creator 13,828 of 14,154 rows-with-column in
// 291 files (97.7%), Reporter 15,540 of 15,881 in 296 files (97.9%).
//
// ⚠⚠ THE PARENT ENTRY CARRIES THREE SPELLINGS AND SHIPPED WITH ONE. `Parent` reports 6,641 rows;
// with the two below the same entry reports 8,407 — **+1,766 rows in 64 files**, every one of which
// arrived with a parent link, wrote parent_id NULL and warned about nothing. `Custom field (Epic
// Link)` is the wider spelling of the two in this corpus (163 files against `Parent`'s 119), because
// company-managed Jira projects put the epic parent in a custom field and Jira's CSV export prefixes
// every custom field that way. `Parent id` is the old Server/DC spelling: 5 files, 18 of those rows.
//
// ⚠ WHAT IS NOT HERE IS MEASURED, NOT OVERLOOKED, AND THE FULL LIST WITH ITS POPULATIONS IS IN
// csv_parent_spellings_test.go. In short: `Parent key`/`Parent summary` add ZERO rows (never
// populated without `Parent`); `Epic Link Summary`/`Custom field (Epic Name)` hold the parent's NAME
// and add ZERO rows; `Epic Issue Key`/`Epic`/`Parent Story`/bare `Epic Link` are 707 rows in 5 files
// that are NOT Jira exports (hand-built spreadsheets — Jira always prefixes custom fields and always
// emits `Issue id`); and `Outward issue link (Parent-Child)` is an issue LINK, which belongs to
// Track's issue_relations table alongside Linear's `Blocked by` and `Related to`, not to parent_id.
var jiraUnreadRefs = []unreadRef{
	{[]string{"Assignee"}, fieldAssigneeRef, viaColumnNotRead},
	{[]string{"Sprint"}, fieldCycleRef, viaColumnNotRead},
	{[]string{"Parent", "Custom field (Epic Link)", "Parent id"}, fieldParentRef, viaColumnNotRead},
	{[]string{"Creator"}, fieldCreatorRef, viaColumnNotReadStamped},
	{[]string{"Reporter"}, fieldCreatorRef, viaColumnNotReadStamped},
}

// unreadRefNotes reports ONE note per ENTRY whose column is populated on this row, naming the
// spelling that held the value.
//
// The gate is the CELL, not the header, and that is what keeps the report worth reading: an export
// that carries an `Assignee` column and no assignees has lost nothing, and a warning on every such
// import is one an operator learns to skip. An ABSENT column is silent here too — that is a real
// condition with its own vocabulary in this package (viaNo*Column), and it is not "a value arrived
// and was dropped".
//
// ⚠ THE CELL GATE IS WHY THE SPELLING LOOP CANNOT STOP AT THE FIRST COLUMN THAT EXISTS. An export
// carrying `Parent` AND `Custom field (Epic Link)` leaves `Parent` empty on 1,748 rows and puts the
// link in the custom field, so a loop that broke on ci.has would go silent on exactly the rows this
// change is about. Presence is checked per spelling and the note is emitted for the first spelling
// that is present AND populated.
func unreadRefNotes(ci columnIndex, row []string, refs []unreadRef) []FieldNote {
	var out []FieldNote
	for _, r := range refs {
		for _, col := range r.columns {
			if !ci.has(col) || ci.get(row, col) == "" {
				continue
			}
			// Value is the COLUMN NAME, never the cell. The tally in run() keys on the whole
			// FieldNote, so putting a cell value here would turn a 10,000-row import into 10,000
			// distinct notes — the exact failure #80's bound exists to prevent — and would spill
			// assignee identities into a job row that is read by anyone who can read the job.
			out = append(out, FieldNote{Field: r.field, Value: col, Via: r.via})
			// ONE note per entry per row. Two spellings of one reference are one dropped value; see
			// the preference-order note on unreadRef.
			break
		}
	}
	return out
}
