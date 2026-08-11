package importer

import (
	"strings"

	"github.com/talyvor/track/internal/model"
)

// csv_done_without_completion.go — the third date column's absent-cases, on both CSV transports.
//
// ⚠⚠ THE DEFECT WAS AN ASYMMETRY, NOT A MISSING PARSE. `Created` and `Updated` each report BOTH
// ways their column fails to produce an instant (viaNo*Column when the export carries no such
// header, viaNo*Value when the header is there and the cell is blank). The completion column —
// Jira's `Resolved`, Linear's `Completed` — reported NEITHER. It reported only a value that
// ARRIVED and was refused (viaUnparseableDate, #111) or arrived on a non-done issue
// (viaStatusNotDone, #74). An export that simply has no completion column produced a done issue
// with completed_at NULL and an import that called itself clean.
//
// ⚠ WHY THAT MATTERS MORE HERE THAN THE NULL SUGGESTS. analytics' resolution and throughput
// queries select on `completed_at IS NOT NULL`. A done issue with a NULL there is not wrong in
// those reports, it is ABSENT from them — it counts as neither open work nor delivered work — and
// unlike created_at/updated_at (TIMESTAMPTZ DEFAULT NOW(), so the loss is a plausible-looking wrong
// value) there is no column anybody can look at to see that it happened.
//
// MEASURED 2026-08-11 with THE SHIPPED mappers over the two corpora this item has used since
// #99/#103 — 346 real Jira exports, 45 real Linear exports — classifying every row that imported
// as `done`:
//
//	jira   : 7,186 done · 4,043 carried a completion instant · 2,288 NULL, COLUMN ABSENT (7 exports)
//	         · 137 NULL, cell empty · 718 NULL and already reported by #111's refusal
//	linear : 1,153 done · 1,119 carried one · 34 NULL, COLUMN ABSENT (1 export)
//
// So 2,425 of 7,186 Jira done issues — 33.7% — and 34 of 1,153 Linear ones were silent. The 718
// were not: the refusal path was already loud, which is exactly why the two absences read as
// covered. See csv_done_without_completion_census_test.go to reproduce the numbers.
//
// ⚠ THE GATE IS THE PART TO READ, NOT THE NOTE. This reports ONLY where status == done. An OPEN
// issue with no completion time is correct and must stay silent — every export in both corpora is
// mostly open work, so an ungated note would fire on essentially every import and the warnings
// channel would stop being read. The condition here is precisely the condition under which the row
// is invisible to `completed_at IS NOT NULL` while claiming to be finished work.
const (
	viaNoResolvedColumn = "no-Resolved-column" // the export has no such header at all
	viaNoResolvedValue  = "no-Resolved-value"  // the header exists and this row's cell is empty

	// Linear's twins. SEPARATE constants rather than one shared pair, for the reason
	// viaNoLinearCreatedColumn is separate from viaNoCreatedColumn: the rendered line names the
	// provider's own column, and a Linear operator sent to look for `Resolved` is being sent to a
	// column their export does not have.
	viaNoLinearCompletedColumn = "no-Linear-Completed-column"
	viaNoLinearCompletedValue  = "no-Linear-Completed-value"
)

// doneWithoutCompletionNote reports a done issue whose completion column supplied no instant.
//
// It takes the whole columnIndex rather than a pre-fetched string for the reason jiraCSVCreated
// does: ci.get answers "" for a missing HEADER and for an empty CELL alike, and those are two
// different findings — a re-export fixes the first and nothing fixes the second.
//
// It returns nothing when the cell HELD something. A value that arrived and was refused is already
// reported by jiraCSVResolved/linearCSVCompleted, and reporting the same loss twice would inflate
// the loudest case in the census.
func doneWithoutCompletionNote(ci columnIndex, row []string, status model.IssueStatus, column, field, viaNoColumn, viaNoValue string) []FieldNote {
	if status != model.StatusDone {
		return nil
	}
	if !ci.has(column) {
		return []FieldNote{{Field: field, Via: viaNoColumn}}
	}
	if strings.TrimSpace(ci.get(row, column)) == "" {
		return []FieldNote{{Field: field, Via: viaNoValue}}
	}
	return nil
}

// The two provider bindings. They are two functions rather than two call-site argument lists so
// that each transport's column, field and Via constants are stated in one place and a mutation to
// either one is a mutation to a named thing.
func jiraCSVDoneWithoutResolved(ci columnIndex, row []string, status model.IssueStatus) []FieldNote {
	return doneWithoutCompletionNote(ci, row, status,
		jiraCSVResolvedColumn, fieldResolutionDate, viaNoResolvedColumn, viaNoResolvedValue)
}

func linearCSVDoneWithoutCompleted(ci columnIndex, row []string, status model.IssueStatus) []FieldNote {
	return doneWithoutCompletionNote(ci, row, status,
		linearCSVCompletedColumn, fieldCompletionTime, viaNoLinearCompletedColumn, viaNoLinearCompletedValue)
}
