package importer

import (
	"strings"
	"time"
)

// jira_csv_updated.go — the column that says WHEN THE ISSUE WAS LAST TOUCHED, and why its absence
// reorders the product's main screen.
//
// ⚠⚠ THIS FIELD IS HERE BECAUSE A STOP REASON WAS WRONG, NOT BECAUSE A FIELD WAS MISSING. #83
// scoped `updated_at` out with:
//
//	"nothing in Track reads updated_at for a report, so it is a smaller defect than this one"
//
// and #84 wrote the same sentence down again, correctly flagging it as A CLAIM NOBODY HAS MEASURED.
// It is false. MEASURED at `d3aaaca` by enumerating READS of the column instead of re-reading the
// importer — five consumers, in two languages:
//
//	frontend/src/components/issue/IssueRow.tsx:58   relativeTime(issue.updated_at) — on EVERY row
//	frontend/src/components/issue/IssueList.tsx:48  sorts the issue list by updated_at DESC
//	internal/issue/store.go:1135                    Search ORDER BY updated_at DESC
//	internal/issue/store.go:648                     updated_at is in the API's sort whitelist
//	internal/analytics/engine.go:416,433,483,508    the AI-cost report's window AND its x-axis,
//	                                                `date_trunc('day', updated_at) AS day`
//
// ⚠ THE LARGEST CONSUMER IS NOT A REPORT — it is the issue list, the product's main screen, which
// orders by recency and prints "updated <n> ago" on every row. That is why "smaller than the
// created_at defect" was the wrong frame: #83's defect corrupted a NUMBER on an analytics page,
// this one reorders the screen the team works from every day.
//
// ⚠ WHY A COLUMN ASSERTION CANNOT SEE IT, inherited from jira_csv_created.go and true again:
// `issues.updated_at` is `TIMESTAMPTZ DEFAULT NOW()` and NEITHER write statement named it, so every
// imported row carried THE INSTANT THE IMPORT RAN — always non-null, always looking populated, the
// wrong value indistinguishable in shape from the right one. Observable only in what the product
// DOES with it, which is what jira_csv_updated_job_test.go asserts.
//
// MEASURED 2026-08-10 against a real Jira's CSV export (jira.atlassian.com, anonymous, the
// issue-navigator "csv-all-fields" view), NEGATIVE-CONTROLLED FIRST so a 200 is not a blanket
// answer — fabricated host ⇒ URLError · fabricated VIEW on the real host ⇒ 400 text/html ·
// fabricated PROJECT in the JQL ⇒ 400 text/html. Re-run with
// scripts/w34-jira-csv-updated-probe.py, which FAILS rather than proceeds if a control answers 200:
//
//	header "Updated"   occurrences: 1  (SINGLE-valued — so `get` is correct here and is pinned as
//	                   correct, unlike the repeated `Labels` header #79 had to fix; 402 columns)
//	bytes              "03/Mar/2025 9:09 AM" — the same rendering as Created/Due Date/Resolved,
//	                   which is why this file reuses parseJiraCSVTime rather than pinning a
//	                   second layout list
//	staleness          days since Updated: min 3.2 · median 2692.0 · max 2855.0 over 200 issues.
//	                   Track recorded 0.0 for every one of them.
//	                   200 of 200 rows (100%) are more than a DAY stale.
//
// ⚠ THE ORDERING OF THAT SAMPLE IS PART OF THE MEASUREMENT AND MY FIRST ONE WAS WRONG. Ordering the
// export `BY updated DESC` — the obvious thing to type — selects the FRESHEST rows in the project
// and answered "median 2.7 days", which is a fact about the query and not about a backlog. The
// number above is `ORDER BY key ASC`, which is neutral with respect to the column being measured.
// Three orders of magnitude apart; only the second one means anything.
//
// ⚠ THE TIMEZONE LIMIT OF jira_csv_dates.go APPLIES HERE UNCHANGED and is inherited deliberately:
// the export carries no offset, so this instant is the exporting user's wall clock read as UTC and
// may be up to ±14h out. On a staleness measured in years that is noise; it is stated so nobody
// discovers it later in a sort order.
const jiraCSVUpdatedColumn = "Updated"

// fieldUpdated names the field in a warning line in the operator's own vocabulary, the same rule
// fieldCreated and fieldCompletionTime follow.
const fieldUpdated = "last-updated time"

// The two ways this column fails to produce an instant, kept APART for the reason established by
// #73 and applied by #83: only the first distinguishes "this code never ran" from "your export had
// nothing to read", and this field's failure is otherwise INVISIBLE — the column is never null, it
// is merely wrong.
const (
	viaNoUpdatedColumn = "no-Updated-column" // the export has no such header at all
	viaNoUpdatedValue  = "no-Updated-value"  // the header exists and this row's cell is empty
)

// jiraCSVUpdated maps the Updated column to the instant the PROVIDER last changed the issue.
//
// It takes the whole columnIndex rather than a pre-fetched string because ci.get answers "" for a
// missing HEADER and for an empty CELL alike, and those are two different reports.
//
// A value the pinned layouts refuse is REPORTED, never silently defaulted — #74's rule. It carries
// the same weight here as it does for Created: the silent default is not a null anybody can spot
// but a plausible-looking timestamp that sorts to the top of the list.
func jiraCSVUpdated(ci columnIndex, row []string) (time.Time, []FieldNote) {
	if len(ci[strings.ToLower(jiraCSVUpdatedColumn)]) == 0 {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNoUpdatedColumn}}
	}
	raw := ci.get(row, jiraCSVUpdatedColumn)
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNoUpdatedValue}}
	}
	t, ok := parseJiraCSVTime(raw)
	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Value: raw, Via: viaUnparseableDate}}
	}
	return t, nil
}
