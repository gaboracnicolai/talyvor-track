package importer

import (
	"strings"
	"time"
)

// jira_csv_created.go — the column that says WHEN THE ISSUE WAS OPENED, and why its absence is a
// wrong number rather than a missing one.
//
// ⚠⚠ THE FACT WAS ALREADY MEASURED AND WRITTEN DOWN IN THIS PACKAGE, ONE FILE AWAY, AND USED FOR
// SOMETHING ELSE. jira_csv_dates.go's header records, as evidence about HOUR PADDING in the layout:
//
//	Created   "07/Aug/2026 12:54 PM" and "09/Aug/2026 8:15 AM" — the hour is NOT zero-padded
//
// #78 observed the column, wrote down its bytes, used them to pin a layout, and never asked what it
// meant that jiraRowMapper does not read it. That is the same shape as #79 (the repeated `Labels`
// header, recorded as evidence about the column COUNT) and #82 (the `Resolution` column, recorded
// as an argument that the completed_at gate was safe). THE EVIDENCE WAS IN THE FILE; THE QUESTION
// WAS NOT — third instance in this item.
//
// ⚠ WHY THIS IS NOT "ONE MORE FIELD". Every other date W3.4 has landed is verifiable as a column:
// #74's own asymmetry note says a landed date is DIRECTLY OBSERVABLE as a non-null value, so "the
// code never ran" and "the provider sent none" are distinguishable by querying. `created_at` is not
// like that. `issues.created_at` is `TIMESTAMPTZ DEFAULT NOW()` and issue.Store.Create's INSERT did
// not name it, so every imported row got the INSTANT THE IMPORT RAN — always non-null, always
// looking populated, and the wrong value is byte-indistinguishable in shape from the right one.
//
// It is observable in exactly one place: the number computed FROM it. Track's analytics reports
// time to resolution as `EXTRACT(EPOCH FROM completed_at - created_at)/3600`, and #74/#78
// deliberately landed `completed_at` FROM THE PROVIDER. So the subtraction was (a past instant) −
// (now). MEASURED on real Postgres through the async runner, an issue Jira opened 200 days ago and
// finished 100 days ago:
//
//	median time to resolution = -2400.0 hours          ← the true answer is +2400
//
// and the import reported {imported:1, skipped:0, refused:0, warnings:[]}. This item's "data loss
// reported as success" shape, ELEVENTH INSTANCE — and the first where the loss surfaces as a
// PUBLISHED NUMBER rather than an empty column.
//
// MEASURED 2026-08-09 against a real Jira's CSV export (jira.atlassian.com, anonymous, the
// issue-navigator "csv-all-fields" view), NEGATIVE-CONTROLLED FIRST so a 200 is not a blanket
// answer — fabricated host ⇒ no resolution · fabricated VIEW on the real host ⇒ 400 text/html ·
// fabricated PROJECT in the JQL ⇒ 400 text/html. Re-run with
// scripts/w34-jira-csv-created-probe.py, which FAILS rather than proceeds if a control answers 200:
//
//	header "Created"   occurrences: 1  (SINGLE-valued — unlike Labels, so `get` is correct here
//	                   and is pinned as correct; 325 columns over 200 resolved issues)
//	bytes              "23/Jul/2026 7:36 PM" — the same rendering as Due Date and Resolved, which
//	                   is why this file reuses parseJiraCSVTime rather than pinning a second list
//	age at import      min 17 · median 332 · max 530 days since Created
//	true cycle time    min 0.0 · median 687.0 · max 11254.7 hours (Resolved − Created)
//	what Track computed instead: min -12661.5 · median -6543.7 · max -86.9 hours. 200 of 200 NEGATIVE.
//
// ⚠ THE TIMEZONE LIMIT OF jira_csv_dates.go APPLIES HERE UNCHANGED and is inherited deliberately:
// the export carries no offset, so this instant is the exporting user's wall clock read as UTC and
// may be up to ±14h out. On a cycle time measured in hundreds of hours that is noise; it is stated
// so nobody discovers it later in a chart.
const jiraCSVCreatedColumn = "Created"

// fieldCreated names the field in a warning line so the sentence reads as the operator's own
// vocabulary, the same rule fieldCompletionTime follows for Linear.
const fieldCreated = "creation time"

// The two ways this column fails to produce an instant, kept APART because they send an operator to
// two different places and because only the first one distinguishes "this code never ran" from
// "your export had nothing to read" — the structural-zero defence #73 established, applied to a
// field whose failure is otherwise INVISIBLE (the column is never null; it is merely wrong).
const (
	viaNoCreatedColumn = "no-Created-column" // the export has no such header at all
	viaNoCreatedValue  = "no-Created-value"  // the header exists and this row's cell is empty
)

// jiraCSVCreated maps the Created column to the instant the PROVIDER opened the issue.
//
// It takes the whole columnIndex rather than a pre-fetched string because ci.get answers "" for a
// missing HEADER and for an empty CELL alike, and those are the two failures that must not be
// reported as one: an export with no Created column at all is a re-export, and a handful of blank
// cells is a data-quality note about those rows.
//
// A value the pinned layouts refuse is REPORTED, never silently defaulted — #74's rule, and it
// carries more weight here than anywhere else in this package, because the silent default is not a
// null anybody can spot but a plausible-looking timestamp that makes every report wrong.
func jiraCSVCreated(ci columnIndex, row []string) (time.Time, []FieldNote) {
	if len(ci[strings.ToLower(jiraCSVCreatedColumn)]) == 0 {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedColumn}}
	}
	raw := ci.get(row, jiraCSVCreatedColumn)
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Via: viaNoCreatedValue}}
	}
	t, ok := parseJiraCSVTime(raw)
	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldCreated, Value: raw, Via: viaUnparseableDate}}
	}
	return t, nil
}
