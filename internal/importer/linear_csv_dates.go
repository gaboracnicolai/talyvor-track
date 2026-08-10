package importer

import (
	"strings"
	"time"

	"github.com/talyvor/track/internal/model"
)

// linear_csv_dates.go — the two date columns the Linear CSV transport has carried in its own
// fixtures since the first Linear test and has never once read.
//
// ⚠⚠ THE STOP REASON WAS TRUE ABOUT THE BYTES AND FALSE ABOUT THE COLUMNS. W3.4 has recorded, for
// several merges, "LINEAR CSV, blocked on an unmeasurable export" — and used it to skip the whole
// transport. Split in two, only half of it survives:
//
//	WHICH COLUMNS      MEASURABLE, AND MEASURED. Linear's own import documentation names the export
//	                   header it produces — Title · Description · Priority · Status · Assignee ·
//	                   Created · Completed · Labels · Estimate — and THIS PACKAGE'S OWN FIXTURES
//	                   have carried exactly that header since csv_test.go was written. The five
//	                   columns linearRowMapper already reads come from the same line. Two of the
//	                   four it ignores are these.
//	WHAT THEY LOOK LIKE  NOT MEASURABLE FROM HERE. No credential in this environment can produce a
//	                   Linear export, so the SERIALISATION is pinned by hand — see below.
//
// So the blocker justified not pinning a layout with confidence. It never justified not READING the
// columns, and reading them is where the defect is.
//
// ⚠ AND THIS IS THE TRANSPORT WITH NOTHING IN FRONT OF IT. #83 (jira_csv created), #84 (both APIs
// created), #85/#86 (updated) and #74/#78 (completed) all landed while `linear_csv` — one of
// exactly two source types job_handler.go's validSourceTypes accepts with NO integration, NO
// credential and NO TRACK_INTEGRATION_ENCRYPTION_KEY — kept both defects. The gated half got the
// merges; the half any member can reach with one upload went unread.
//
// MEASURED END TO END through the async runner on real Postgres (linear_csv_dates_job_test.go), for
// an issue Linear opened 200 days before the import and finished 100 days before it:
//
//	created_at    the IMPORT INSTANT — off by 4804h — never null, never empty, exactly the shape a
//	              correct value has
//	completed_at  NULL, so the row does not fail analytics' `completed_at IS NOT NULL` predicate —
//	              it is ABSENT from the resolution and throughput reports altogether
//	the report    median time to resolution = 0 for a workspace whose only issue took 100 days
//	the job row   {status:"succeeded", imported:1, warnings:[]}
//
// This item's "data loss reported as success" shape, TWELFTH instance — and the first where the two
// halves hide each other: an unread `Created` alone shows up as a NEGATIVE cycle time (#83), but
// with `Completed` also unread there is no row to compute it on, so the number never appears at all.

// The two column spellings, exactly as Linear's own importer documentation names them and as this
// package's fixtures have always written them. They are looked up case-insensitively (buildIndex
// lowercases both sides), so "created" resolves too.
//
// ⚠ THEY ARE THIS TRANSPORT'S OWN CONSTANTS EVEN THOUGH `Created` IS SPELLED THE SAME ON THE JIRA
// SIDE. A warning line names the column an operator has to go and look at; pointing a Linear
// operator at a constant called jiraCSVCreatedColumn makes the two spellings move together forever
// after, and they are facts about two different products. Same argument as viaStateType being
// separate from viaCategory.
const (
	linearCSVCreatedColumn   = "Created"
	linearCSVCompletedColumn = "Completed"
)

// linearCSVTimeLayouts are pinned BY HAND, in the order tried.
//
// ⚠⚠ THE PROVENANCE IS THE WEAKEST IN THIS PACKAGE AND IS NOT DRESSED UP AS EQUAL TO ANY OF THE
// OTHER THREE LISTS. jiraCSVTimeLayouts was pinned from the BYTES of a real export.
// jiraTimeLayouts from the BYTES of a real API response. linearTimeLayouts from the DECLARED SCALAR
// TYPES of Linear's schema, read by unauthenticated introspection. This one has neither: nothing
// reachable from here emits a Linear CSV export.
//
// ⚠ AND parseLinearTime IS DELIBERATELY NOT REUSED, WHICH IS THE POINT RATHER THAN A DUPLICATION
// SLIP. Its layouts were derived from Issue.completedAt : DateTime and Issue.dueDate :
// TimelessDate — facts about the GRAPHQL surface. Calling it here would lend a CSV export the
// evidence an API measurement gathered, and THIS PACKAGE ALREADY HOLDS THE COUNTEREXAMPLE: for
// Jira the two surfaces do not agree even slightly —
//
//	API    "2026-08-06T20:06:39.000+0000"     (jiraTimeLayouts)
//	CSV    "23/Jul/2026 7:36 PM"              (jiraCSVTimeLayouts)
//
// One provider rendering the same instant two completely different ways is the measured reason not
// to assume the other provider renders it one way. The two lists happen to overlap today; they are
// separate so that a future measurement of a real Linear export changes THIS list and nothing else.
//
// ⚠ WHICH IS WHY THE REFUSAL IS THE LOAD-BEARING PART, NOT THE LIST. A value no layout accepts is
// REPORTED, never silently defaulted — so a tenant whose export differs from both shapes below
// learns it on its first import, in the warnings channel, instead of receiving a column of
// import-instant timestamps that reads as a working import.
//
// ⚠⚠ AND THE PARAGRAPH ABOVE IS NOW STALE IN ITS PREMISE, WHICH IS WHY THE LIST HAS A THIRD ENTRY.
// "Nothing reachable from here emits a Linear CSV export" was true when it was written and stopped
// being true at #99, which found 45 real exports unrelated tenants had committed to public
// repositories. Measured against those bytes, the two hand-pinned layouts below accept 4,698 of
// 5,440 distinct date cells and REFUSE 734 — a quarter of every `Created` and `Updated` column in
// the corpus, from six owners who have never met. The list is what this file said it was: a
// hand-pinned guess to be replaced by a measurement. See linear_csv_tostring_dates.go for the
// population, the owner attribution, and what is deliberately NOT claimed about it.
//
// ⚠ THE FIRST ENTRY IS THE OTHER HALF OF THE SAME POINT AND IS LEFT ALONE DELIBERATELY: zero cells
// in the whole corpus are date-only. It is pinned from this package's FIXTURES, it matches nothing
// a real tenant emits, and it is kept because removing a layout can only refuse a shape somebody
// might hold — which is a different decision from adding one, and not this merge's.
var linearCSVTimeLayouts = []string{
	"2006-01-02",                // the shape this package's fixtures have always carried, and what Linear's docs call a "created date"
	time.RFC3339,                // Linear's docs call Completed a "timestamp"; its API renders DateTime this way
	linearCSVDateToStringLayout, // ECMAScript Date.prototype.toString — 746 of 2,947 real `Updated` cells
}

// parseLinearCSVTime returns the instant and true, or false if no pinned layout accepts the value.
// A false is REPORTED by the caller, never silently nil'd — that is what keeps a hand-pinned list
// honest, and it carries more weight here than anywhere else in this package because this list was
// the one nobody had been able to check against a real export.
func parseLinearCSVTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// The optional, implementation-defined zone NAME that ECMA-262 appends after the numeric
	// offset. Stripped BEFORE the loop rather than inside a fourth layout because Go's MST verb
	// reads an abbreviation and the corpus carries two spellings, only one of which is one —
	// see jsDateToStringZoneName for why the rule is anchored so tightly that no other shape in
	// this file's history can reach it.
	s = stripJSDateToStringZoneName(s)
	for _, layout := range linearCSVTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// viaNoLinearCreatedColumn is the structural-zero line for a field whose failure is otherwise
// INVISIBLE: created_at is never null, it is merely wrong, so without this an operator cannot tell
// "Track read your Created column" from "Track recorded every one of these as opened today".
//
// It is a separate constant from viaNoCreatedColumn because its rendered sentence names
// linearCSVCreatedColumn — see the column constants above. The two remaining shapes (an empty CELL,
// and a value no layout accepts) render provider-neutrally, so they reuse viaNoCreatedValue and
// viaUnparseableDate rather than inventing a second spelling of the same sentence.
const viaNoLinearCreatedColumn = "no-Linear-Created-column"

// linearCSVCreated maps the Created column to the instant the PROVIDER opened the issue.
//
// It takes the whole columnIndex rather than a pre-fetched string because ci.get answers "" for a
// missing HEADER and for an empty CELL alike, and those two must not be reported as one: an export
// with no Created column at all is the wrong file, and a handful of blank cells is a data-quality
// note about those rows.
func linearCSVCreated(ci columnIndex, row []string) (time.Time, []FieldNote) {
	if len(ci[strings.ToLower(linearCSVCreatedColumn)]) == 0 {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoLinearCreatedColumn}}
	}
	raw := ci.get(row, linearCSVCreatedColumn)
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedValue}}
	}
	t, ok := parseLinearCSVTime(raw)
	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Value: raw, Via: viaUnparseableDate}}
	}
	return t, nil
}

// linearCSVCompleted maps the Completed column and refuses it unless the row imported as done —
// #74's decision, inherited rather than re-litigated: Track's issue.Store.Update stamps completed_at
// only on a transition ONTO done and clears it on any transition away, and analytics'
// resolution-stats query selects on `completed_at IS NOT NULL` with NO status predicate, so an
// abandoned issue carrying one counts as delivered work.
//
// ⚠ THE REFUSAL IS REACHABLE ON THIS TRANSPORT AND THAT IS MEASURED FROM THE MAPPER RATHER THAN
// ASSUMED: mapLinearStatus reads "Cancelled"/"Canceled" as model.StatusCancelled, and Linear gives
// cancellation its own terminal state, so a cancelled row carrying a Completed value is an ordinary
// export line rather than a contrived one.
//
// ⚠ AN ABSENT Completed COLUMN IS NOT REPORTED, AND THE ASYMMETRY WITH Created IS THE WHOLE REASON
// THIS CHANNEL IS WORTH ANYTHING. An unread Created produces a WRONG value that looks right; an
// unread Completed produces an honest NULL that no report misreads. Warning about the second would
// put a line nobody can act on into the channel the first one has to be read in.
func linearCSVCompleted(raw string, status model.IssueStatus) (*time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, ok := parseLinearCSVTime(raw)
	if !ok {
		return nil, []FieldNote{{Field: fieldCompletionTime, Value: raw, Via: viaUnparseableDate}}
	}
	if status != model.StatusDone {
		return nil, []FieldNote{{Field: fieldCompletionTime, Value: raw, Mapped: string(status), Via: viaStatusNotDone}}
	}
	return &t, nil
}
