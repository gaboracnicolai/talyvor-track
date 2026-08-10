package importer

import (
	"strings"
	"time"

	"github.com/talyvor/track/internal/model"
)

// jira_csv_dates.go — the two dates a Jira CSV EXPORT carries, and the layouts it serialises them in.
//
// ⚠ THIS IS DELIBERATELY NOT parseJiraTime, AND THE SEPARATION IS THE POINT. jira.go's layouts were
// pinned from the BYTES A REAL JIRA API RESPONSE SENT (#74) — `"2027-12-31"` and
// `"2026-08-06T20:06:39.000+0000"`. A CSV export of the same instance emits neither shape. Sharing
// the helper would lend the API's observed-bytes provenance to a transport whose bytes are different,
// which is the overclaim #75 already caught in this package once; and it would not have worked
// either, which TestJiraCSVTime_TheAPILayoutsRefuseEveryMeasuredCSVDate asserts in both directions.
//
// MEASURED 2026-08-09 against a real Jira's CSV export (jira.atlassian.com, anonymous, the
// issue-navigator "csv-all-fields" view). NEGATIVE-CONTROLLED FIRST, so the 200 is not a blanket
// answer: fabricated host ⇒ curl 000 · fabricated VIEW name on the real host ⇒ 400 text/html ·
// fabricated PROJECT in the JQL ⇒ 400 text/html. Re-run it with
// scripts/w34-jira-csv-export-probe.py.
//
//	header    the two columns this file reads are spelled "Due Date" and "Resolved"
//	          (the column COUNT is not pinned anywhere: an all-fields export repeats
//	          multi-value columns per row, so the same view answered 212 and 279 for two
//	          different result sets — a count would be a number that drifts on its own)
//	Due Date  "19/Jan/2025 12:00 AM"   — a midnight LOCAL time, never a bare date
//	Resolved  "25/Mar/2025 10:03 AM"
//	Created   "07/Aug/2026 12:54 PM" and "09/Aug/2026 8:15 AM" — the hour is NOT zero-padded
//
// ⚠ TWO LIMITS, STATED RATHER THAN IMPLIED, BECAUSE BOTH ARE UNFIXABLE FROM INSIDE THE FILE:
//
//  1. THE FORMAT IS A PER-INSTANCE PREFERENCE. Jira renders CSV dates with the instance's
//     look-and-feel date format, so another tenant's export may not be this shape at all. That is
//     exactly why a value no pinned layout accepts is REPORTED and never nil'd (#74's rule): a
//     tenant whose serialisation differs learns it on their first import instead of receiving a
//     column of nulls that reads as "we have no due dates". The list stays as small as the
//     measurement; TestJiraCSVTime_TheLayoutListIsExactlyWhatWasMeasured fails on a layout added
//     without bytes behind it.
//
//     ⚠⚠ THAT PARAGRAPH WAS A STATED LIMIT, NOT A MEASUREMENT, AND WHEN IT WAS FINALLY MEASURED
//     THE ONE-ENTRY LIST TURNED OUT TO REFUSE 91.9% OF THE POPULATION. "another tenant's export
//     may not be this shape at all" was written as a caveat and read for four merges as a
//     residual risk; the corpus that would have answered it was ALREADY CACHED for this
//     package's status-category and priority censuses. Applying THE SHIPPED parseJiraCSVTime to
//     every cell of the four date columns of 301 real exports: 3,268 accepted, 37,255 REFUSED,
//     and 298 of 301 files carry at least one refused cell. Per column — Created 90.4% refused
//     (296 of 299 files lose EVERY cell), Updated 89.4% (290 of 293), Due Date 100.0% (119 of
//     119), Resolved 100.0% (178 of 178). See jira_csv_two_digit_year.go for the shape census,
//     for what is still refused and why it is NOT guessed at, and for the compound silence the
//     two losses produce together.
//
//  2. THE EXPORT CARRIES NO TIMEZONE. "25/Mar/2025 10:03 AM" is rendered in the EXPORTING USER's
//     timezone and the offset is nowhere in the file, so the instant below is that wall-clock time
//     read as UTC and may be up to ±14h from the true instant. Nothing in the CSV can close this —
//     the API transport, which does carry an offset, is the fix for anyone who needs the exact
//     instant. For `Due Date`, which Jira always renders at midnight, the DATE is the meaningful
//     part and survives; for `Resolved` it is a real approximation and is recorded here rather than
//     discovered later in a cycle-time chart.
//
// Each entry carries the number of cells IN THE CORPUS that it is the first to accept, so no line
// here is a guess. Three further shapes were evaluated and DROPPED for scoring zero — an entry with
// no bytes behind it is exactly what limit (1) above forbids.
var jiraCSVTimeLayouts = []string{
	"2/Jan/2006 3:04 PM",  //  3,268 — the original probe's shape; `2` and `3` also tolerate a padded day/hour
	"2/Jan/06 3:04 PM",    // 27,147 — the same shape with a two-digit year, and the single commonest in the corpus
	"2/Jan/06 15:04",      //  2,133 — two-digit year, 24-hour clock
	"2006-01-02 15:04",    //  1,228 — ISO-ish, space-separated
	"2006-01-02 15:04:05", //    360 — the same with seconds
	"Jan/2/06 3:04 PM",    //    212 — month-first, two-digit year
	"2/Jan/06",            //     10 — date only; Jira renders Due Date at midnight, so some instances drop the time
}

// parseJiraCSVTime returns the instant and true, or false if no pinned layout accepts the value. A
// false is REPORTED by the caller, never silently nil'd — that is what keeps a hand-pinned list
// honest when the next tenant's export does not match it.
func parseJiraCSVTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range jiraCSVTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// The two column spellings, exactly as the measured export headers them. They are looked up
// case-insensitively (buildIndex lowercases both sides), so "Due date" resolves too.
const (
	jiraCSVDueDateColumn  = "Due Date"
	jiraCSVResolvedColumn = "Resolved"
)

// jiraCSVDueDate maps the Due Date column. Absent is not a loss and is not reported; a value in a
// shape no pinned layout accepts IS a loss, and is.
func jiraCSVDueDate(raw string) (*time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, ok := parseJiraCSVTime(raw)
	if !ok {
		return nil, []FieldNote{{Field: fieldDueDate, Value: raw, Via: viaUnparseableDate}}
	}
	return &t, nil
}

// jiraCSVResolved maps the Resolved column and refuses it unless the row imported as done — #74's
// decision, inherited rather than re-litigated, and it matters MORE on this transport than on the
// API one: a Jira CSV export carries `Resolution` for cancelled work too ("Won't Do", "Cannot
// Reproduce" — both observed on the real instance), and every one of those rows has a Resolved date.
func jiraCSVResolved(raw string, status model.IssueStatus) (*time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, ok := parseJiraCSVTime(raw)
	if !ok {
		return nil, []FieldNote{{Field: fieldResolutionDate, Value: raw, Via: viaUnparseableDate}}
	}
	if status != model.StatusDone {
		return nil, []FieldNote{{Field: fieldResolutionDate, Value: raw, Mapped: string(status), Via: viaStatusNotDone}}
	}
	return &t, nil
}
