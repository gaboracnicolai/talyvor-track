package importer

import (
	"strings"
	"time"
)

// linear_csv_updated.go — the column that says WHEN LINEAR LAST TOUCHED THE ISSUE, on the ONE
// transport of the four that was still dropping it.
//
// ⚠⚠ THIS IS AN ASYMMETRY, NOT A MISSING FEATURE, AND THAT IS WHY IT IS A DEFECT RATHER THAN AN
// UNDECIDED CONTRACT. The same fact reaches Track four ways and three of them keep it:
//
//	jira_csv     jiraCSVUpdated   (#85, `342fa01`)
//	jira_api     apiUpdated       (#86, `313ce80`)
//	linear_api   apiUpdated       (#86, `313ce80`)
//	linear_csv   — nothing —      until this file
//
// #97 made exactly this argument one package over: one seam, two consumers, opposite handling, and
// the disagreement is what turns "we never decided" into "one of them is wrong".
//
// ⚠⚠ AND IT WENT UNCOUNTED BECAUSE THE CENSUS INSTRUMENT WAS THE FIXTURE. #89 enumerated the
// columns this transport ignores — `ID`, `Estimate`, and `Assignee` deferred — and the queue then
// recorded "Created/Completed ARE NOW DONE ON ALL FOUR TRANSPORTS" with a remaining-fields list
// that does not mention `Updated` anywhere. That enumeration was taken from THIS PACKAGE'S OWN
// FIXTURES, whose Linear header is the nine columns Linear's import documentation names:
//
//	Title · Description · Priority · Status · Assignee · Created · Completed · Labels · Estimate
//
// There is no `Updated` in that line, so no fixture in this package could carry the value and no
// test in this package could fail when it was dropped. The documented IMPORT header is not the
// EXPORT header, and reading the columns off the wrong one is how a field stays invisible for
// thirty-one merges.
//
// ⚠ MEASURED AGAINST REAL EXPORT BYTES by scripts/w34-linear-csv-updated-probe.py, over the corpus
// #99 opened — Linear CSV exports that unrelated tenants committed to PUBLIC repositories — with
// the same three negative controls run first (a fabricated column set must find 0 files, a
// fabricated repository must refuse, a fabricated path in a real repository must refuse), because a
// search that quietly returns nothing looks exactly like a search that returns a clean answer:
//
//	45 files parsed as Linear exports · 3,026 data rows · 6 distinct header shapes (29/30/34 cols)
//	`Updated` present in header      44 of 45 files, in ALL SIX header shapes
//	`Updated` non-empty cells        2,947 of 3,026 rows (97.4%)
//	emitted by                       every owner in the corpus
//
// ⚠ THE PROVENANCE IS SECOND-HAND BYTES AND IS NOT DRESSED UP AS EQUAL to the Jira probe's
// first-hand ones — #75's overclaim, the one worth not repeating. What makes it evidence is
// AGREEMENT ACROSS TENANTS THAT HAVE NEVER MET, six header shapes wide, and the fail-safe #99
// relied on holds here unchanged: buildIndex matches the FULL header, so an export that does not
// carry this column yields EXACTLY today's behaviour and cannot be made worse by this file.
//
// ⚠ WHY A COLUMN ASSERTION CANNOT SEE THE LOSS, inherited from #83/#85 and true a fourth time:
// `issues.updated_at` is `TIMESTAMPTZ DEFAULT NOW()`, so an unsupplied value is never null and
// never looks empty — the wrong value has exactly the shape of the right one. The consumers are
// enumerated in jira_csv_updated.go and are unchanged; the largest is not a report but the issue
// list, the product's main screen, which orders by `updated_at DESC` and prints "updated <n> ago"
// on every row. MEASURED end to end through the async runner on real Postgres
// (linear_csv_updated_job_test.go) for an issue Linear last touched 200 days before the import:
//
//	updated_at   the IMPORT INSTANT — off by 4800h
//	the list     the 200-day-stale import ranks ABOVE work created during the test, in
//	             issue.Store.Search, the query the product lists by
//	the job row  {status:"succeeded", imported:1, warnings:[]}
const linearCSVUpdatedColumn = "Updated"

// viaNoLinearUpdatedColumn is the structural-zero line for a field whose failure is otherwise
// INVISIBLE: updated_at is never null, it is merely wrong.
//
// It is a SEPARATE constant from viaNoUpdatedColumn for the reason viaNoLinearCreatedColumn is
// separate from viaNoCreatedColumn: the rendered sentence names linearCSVUpdatedColumn, and a
// Linear operator sent to a constant called jiraCSVUpdatedColumn is one rename away from being sent
// to look at the wrong column entirely. The two remaining shapes — an empty CELL and a value no
// layout accepts — render provider-neutrally and reuse viaNoUpdatedValue and viaUnparseableDate
// rather than inventing a second spelling of the same sentence.
const viaNoLinearUpdatedColumn = "no-Linear-Updated-column"

// linearCSVUpdated maps the Updated column to the instant the PROVIDER last changed the issue.
//
// It takes the whole columnIndex rather than a pre-fetched string because ci.get answers "" for a
// missing HEADER and for an empty CELL alike, and those two must not be reported as one: an export
// with no Updated column is a different file from an export with blank cells in it.
//
// It parses with parseLinearCSVTime — THIS transport's layouts — and deliberately not with
// parseJiraCSVTime. The two lists are kept apart in linear_csv_dates.go on measured grounds (one
// provider renders the same instant two completely different ways on its API and in its CSV), and
// reusing the Jira parser here would lend a Linear export the evidence a Jira measurement gathered.
//
// ⚠ A VALUE THE PINNED LAYOUTS REFUSE IS REPORTED, NEVER SILENTLY DEFAULTED, and that fail-safe is
// unchanged. What changed is WHICH values they refuse: the probe above measured 746 of 2,947 real
// `Updated` cells (25.3%, from six unrelated owners) in `Sun May 11 2025 07:43:48 GMT+0000 (GMT)`,
// JavaScript's Date.toString, and this comment used to end "whether that is Linear's own export or
// a re-serialisation those repositories performed is NOT decidable from here".
//
// ⚠⚠ IT WAS DECIDABLE, AND THE INSTRUMENT WAS THE HEADER RATHER THAN THE DATE. A re-serialiser
// would have had to reproduce Linear's export header byte for byte. Measured: gong8's toString
// export and the ISO exports of amo-tech-ai, UIT6 and null-hype carry the SAME 34 columns in the
// SAME order — down to `Project Milestone ID`, `SLA Status`, `UUID`, `Time in status (minutes)`,
// `Related to`, `Blocked by`, `Duplicate of` — and wubin28's toString export is Linear's OTHER
// published shape, the 30-column one ending in `Roadmaps`. One exporter, four unrelated tenants,
// two date renderings. So the shape is Linear's, rendered through a JS `Date` in some tenants and
// as ISO in others, and the caution that justified refusing it no longer applies. That is the
// "better provenance" TestLinearCSVUpdated_Rule2 wrote down as its own condition for widening.
// See linear_csv_tostring_dates.go for the parse and for what is still NOT claimed.
func linearCSVUpdated(ci columnIndex, row []string) (time.Time, []FieldNote) {
	if len(ci[strings.ToLower(linearCSVUpdatedColumn)]) == 0 {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNoLinearUpdatedColumn}}
	}
	raw := ci.get(row, linearCSVUpdatedColumn)
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Via: viaNoUpdatedValue}}
	}
	t, ok := parseLinearCSVTime(raw)
	if !ok {
		return time.Time{}, []FieldNote{{Field: fieldUpdated, Value: raw, Via: viaUnparseableDate}}
	}
	return t, nil
}
