package importer

import (
	"strings"

	"github.com/talyvor/track/internal/model"
)

// jira_csv_resolution.go — the column a Jira CSV export uses to say whether resolved work was
// FINISHED or ABANDONED.
//
// ⚠ THE FACT WAS ALREADY WRITTEN DOWN ONE FILE AWAY. jiraCSVResolved's own header in
// jira_csv_dates.go argues that the completed_at gate matters on this transport because "a Jira CSV
// export carries `Resolution` for cancelled work too ("Won't Do", "Cannot Reproduce" — both observed
// on the real instance), and every one of those rows has a Resolved date". Nobody asked whether the
// gate ever CATCHES one. It does not: those rows are `Status = Closed`, mapJiraStatus maps "closed"
// to done, the gate passes, and the completion time lands on work that was abandoned.
//
// MEASURED 2026-08-09 against a real Jira (jira.atlassian.com, anonymous REST + the issue-navigator
// "csv-all-fields" export view), negative-controlled first so no 200 is a blanket answer — see
// scripts/w34-jira-csv-resolution-probe.py, which FAILS rather than proceeds if a control answers:
//
//	project JRASERVER, resolved issues                        43,687
//	  ... whose Status maps to Track `done`                   43,587
//	  ... AND whose Resolution says the work was ABANDONED    26,649   ← 61% of the import
//	  ... of those, carrying a Resolved date                  26,649   (all of them)
//	issues whose STATUS is "Cancelled" — the only cancellation
//	  signal mapJiraStatus can see today                           0
//
// So importing that project reports {imported:43587, skipped:0, warnings:[]} while 61% of what it
// recorded as delivered was abandoned, each row carrying a completion time that analytics'
// resolution-stats query counts as throughput and cycle time — it selects on
// `completed_at IS NOT NULL` with NO status predicate.
//
// ⚠ TRACK ALREADY DECLARES WHAT THOSE WORDS MEAN; IT DECLARES IT ABOUT THE WRONG COLUMN.
// mapJiraStatus ships `case "cancelled", "canceled", "won't do", "won't fix"` and reads the STATUS
// column. Measured against /rest/api/2/status, "Won't Do" and "Won't Fix" are not statuses on that
// instance at all — they are RESOLUTIONS, on 5,373 and 6,498 issues.
//
// ⚠⚠ SO THE CLASSIFIER IS mapJiraStatus ITSELF AND THIS MERGE INVENTS NO VOCABULARY. That is the
// load-bearing decision. The resolution word is looked up in Track's own shipped word→status table
// and exactly three things can happen:
//
//	maps to StatusCancelled     the row imports as cancelled instead of done, and #74's existing
//	                            gate then correctly withholds the completion time. REPORTED.
//	maps to StatusDone          it agrees with the status. Nothing changes, nothing is said —
//	                            silence here is what keeps the report readable.
//	anything else, or unknown   NOTHING CHANGES and it is REPORTED with a count.
//
// ⚠ THE THIRD CLASS IS A REFUSAL, NOT AN OVERSIGHT, and it is where the remaining decision lives.
// "Duplicate" (4,938 issues), "Timed out" (3,528), "Obsolete" (1,073), "Not a bug" (669), "Cannot
// Reproduce" (632), "Invalid" (592), "Tracked Elsewhere" (548), "Answered" (1,860), "Handled by
// Support" (550) and "Fixed" (13,411) all land here. Several of them plainly describe abandoned
// work — and deciding which is a product judgement with those numbers behind it, not a session's.
// #76 already refused Linear's `duplicate` state type on exactly this reasoning ("Track has no
// counterpart and answering it invents the meaning this change exists to stop inventing"), and
// overturning that from the sibling transport under cover of a fidelity fix would be worse than
// leaving it open. The report puts the words and the counts in front of a human on the FIRST import
// instead of a session guessing thirteen mappings.
//
// ⚠ IT CAN ONLY EVER MOVE done → cancelled. A Resolution on a row that did not import as done is
// Jira's own inconsistency, and reinterpreting it is not this importer's business.
//
// ⚠ TWO LINES, NOT ONE, WHEN THE OVERRIDE FIRES — stated because it is a choice. The override
// reports itself, and jiraCSVResolved then separately reports the completion time it refused
// (#74's viaStatusNotDone line, now reading "the issue imported as cancelled"). Both sentences are
// true and each is self-describing; suppressing the second would mean carrying state between two
// independent mappers so the report could be one line shorter. #80's bound keeps the date line to
// ten exemplars plus a count however many rows are involved.
//
// ⚠ THE LINEAR CSV PATH IS DELIBERATELY UNTOUCHED. Nothing in this environment can fetch a Linear
// CSV export — it is produced in-app behind authentication — so whether it even carries a
// resolution-shaped column is UNMEASURED, and guessing a column spelling is #75's move.
// TestJiraCSVResolution_LinearCSVIsUntouched pins that this rule does not reach it.

// jiraCSVResolutionColumn is the header spelling measured on the real export. buildIndex lowercases
// both sides, so "resolution" resolves too.
const jiraCSVResolutionColumn = "Resolution"

// fieldResolution names the field in a warning line, so the sentence reads as the operator's own
// column name rather than an internal term.
const fieldResolution = "resolution"

const (
	// The row imported as cancelled because Track's own vocabulary reads the resolution that way.
	viaResolutionCancelled = "resolution-cancelled"
	// The resolution arrived and Track could not read it as finished-or-abandoned. Nothing changed.
	viaResolutionUnreadable = "resolution-unreadable"
)

// applyJiraResolution is the whole rule. It takes the status the NAME mapping produced and the
// raw resolution word, and returns the status the row should import as plus whatever that decision
// needs to say out loud.
//
// ⚠ IT IS SHARED WITH THE JIRA API TRANSPORT AND WAS RENAMED FOR IT (it was applyJiraCSVResolution
// through #82). The rule is transport-neutral — it classifies a WORD through Track's own
// mapJiraStatus and its two warning sentences name no column — so the API path CALLS it rather than
// copying it: two copies would be two vocabularies that can drift, which is the one thing this rule
// exists to prevent. What does NOT transfer is the measurement above: the API transport's evidence
// was gathered separately, on a different Jira product through a different endpoint, and it lives
// in api_resolution.go with the states only that transport has.
//
// Absent column or empty cell ⇒ (status, nil): an unresolved issue has no resolution, and a CSV
// without the column must behave exactly as it did before this file existed.
func applyJiraResolution(raw string, status model.IssueStatus) (model.IssueStatus, []FieldNote) {
	raw = strings.TrimSpace(raw)
	if raw == "" || status != model.StatusDone {
		return status, nil
	}
	// Track's OWN word→status table, reused rather than duplicated — now reached through
	// mapJiraResolution, which consults the RESOLUTION-only vocabulary first and falls back to it.
	// A word neither table knows falls to StatusBacklog, which is neither done nor cancelled, so it
	// takes the "cannot read it" branch below along with every recognised word that means neither
	// (an "Open"-shaped resolution, say).
	//
	// ⚠ THE INDIRECTION IS THE FIX, NOT A TIDY-UP. Reusing the STATUS table alone made "Fixed" —
	// 19,698 of 29,512 resolved issues (66.7%) on a real Cloud instance — fall to the branch that
	// tells the operator Track could not tell whether delivered work was delivered. See
	// jira_resolution_delivered.go for the whole-population measurement, for the provider's own
	// description of each word, and for why exactly one word was added.
	meaning := mapJiraResolution(raw)
	switch meaning {
	case model.StatusCancelled:
		return model.StatusCancelled, []FieldNote{{
			Field:  fieldResolution,
			Value:  raw,
			Mapped: string(model.StatusCancelled),
			Via:    viaResolutionCancelled,
		}}
	case model.StatusDone:
		// Agrees with the status. Nothing to change and nothing to say.
		return status, nil
	default:
		return status, []FieldNote{{
			Field:  fieldResolution,
			Value:  raw,
			Mapped: string(status),
			Via:    viaResolutionUnreadable,
		}}
	}
}
