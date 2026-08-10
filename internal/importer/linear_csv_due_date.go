package importer

import (
	"strings"
	"time"
)

// linear_csv_due_date.go — WHEN LINEAR SAYS THE WORK IS DUE, on the one transport of four that
// never read it.
//
// ⚠ THE COLUMN WAS PINNED SHUT ON PURPOSE AND THE PIN NAMED ITS OWN EXPIRY. #82's
// TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers asserted `DueDate == nil` and said
// why: "the documented export HAS NO DUE-DATE COLUMN AT ALL. So the gap this now pins is the one
// that is still real — a due date read out of some neighbouring column would be invented … Whoever
// gets a real Linear export re-measures the layouts; nobody should make this mapper grow a due date
// without one." That was correct on the evidence it had, and it is the reason this file exists
// rather than a line quietly added to the mapper.
//
// ⚠⚠ THE WORD DOING THE WORK IN THAT REASON WAS "documented", AND IT MEANT THE WRONG DOCUMENT.
// Linear's IMPORT documentation names nine columns — Title · Description · Priority · Status ·
// Assignee · Created · Completed · Labels · Estimate — and there is no due date in that line. This
// package's fixtures have carried that exact header since csv_test.go was written, so no fixture
// here could hold the value and no test here could fail when it was dropped. Linear's EXPORT header
// is a different header. This is #86's finding a second time on a second column: THE DOCUMENTED
// IMPORT HEADER IS NOT THE EXPORT HEADER.
//
// ⚠ MEASURED AGAINST REAL EXPORT BYTES by scripts/w34-linear-csv-column-census-probe.py over the
// corpus #99 opened — Linear CSV exports unrelated tenants committed to PUBLIC repositories. That
// probe does not start from a column name, which is the point: it reads the FULL HEADER of every
// real export and subtracts the set linearRowMapper reads, so the drop list comes from the
// product's input instead of from its fixtures. Four negative controls first, the fourth being one
// the three earlier probes do not have (a search that quietly returns nothing looks exactly like a
// search that returns a clean answer, and a census with a miswired counter looks exactly like a
// census of a column that is really there):
//
//	N1 a fabricated column set     ⇒ 0 files      N3 a fabricated path in a real repo ⇒ REFUSED
//	N2 a fabricated repository     ⇒ REFUSED      N4 a fabricated COLUMN NAME         ⇒ 0 headers, 0 cells
//
//	45 files · 17 distinct owners · 3,099 data rows · 6 header shapes (29/30/34 cols)
//	`Due Date` in header      45 of 45 files, ALL SIX header shapes, all 17 owners
//	`Due Date` non-empty      447 of 3,099 rows (14.4%)
//
// ⚠ NO NEW LAYOUT IS PINNED BY THIS FILE, AND THAT IS A MEASUREMENT RATHER THAN AN ASSUMPTION.
// 441 of the 447 real cells are in two shapes parseLinearCSVTime already accepts — 408 ISO-8601
// with milliseconds and a `Z` (3 owners), 33 ECMAScript Date.prototype.toString across both zone-
// name spellings (3 owners). Six unrelated owners, two renderings, zero additions to
// linearCSVTimeLayouts. The remaining 6 are the literal text "Due Date" — a leaked header row, the
// same one the Status census counted — and those are REFUSED and reported, not coerced.
//
// ⚠ THE PROVENANCE IS SECOND-HAND BYTES and is not dressed up as equal to the Jira probe's
// first-hand ones (#75's overclaim). What makes it evidence is AGREEMENT ACROSS TENANTS THAT HAVE
// NEVER MET, and the fail-safe every column file in this package relies on holds here unchanged:
// buildIndex matches the FULL header, so an export that does not carry this column yields EXACTLY
// today's behaviour and cannot be made worse by this file.
//
// ⚠ WHAT THIS DOES **NOT** FIX, STATED PLAINLY SO THE NEXT SESSION DOES NOT READ IT AS DONE.
// `due_date`'s only LOGIC consumer in this repo is analytics.GetWorkload's `overdue` counter, and
// that query is `FROM issues i JOIN members m ON m.id = i.assignee_id` — an INNER join. NO import
// transport maps Assignee (measured: zero occurrences of AssigneeID in internal/importer outside
// tests), so every imported issue has a NULL assignee and contributes nothing to Workload no matter
// what its due_date says. Reading this column makes the STORED ROW true — it does not light up the
// counter, and the counter is still structurally zero for imported work. The assignee gap is the
// larger finding and it needs a decision, not a patch: the Linear CSV `Assignee` cell is an email
// (1,363 of 3,099 real rows, 44.0%, 17 owners) and resolving it to a Track member means wiring a
// member store into the importer and choosing what happens when no member matches.
const linearCSVDueDateColumn = "Due Date"

// linearCSVDueDate maps the Due Date column to the instant Linear says the work is due.
//
// ⚠ ABSENT IS NOT A LOSS AND IS NOT REPORTED — the asymmetry against linearCSVCreated and
// linearCSVUpdated, which report every absence. Those two land in `created_at` / `updated_at`,
// which are DEFAULT NOW(): an unsupplied value there is not an empty column but a WRONG one that
// looks right, so silence would be a lie. `due_date` is NULLABLE, so an absent value leaves a
// TRUTHFUL null, and reporting it would put a degradation warning on the 2,652 real rows (85.6%)
// whose cell is legitimately empty. jiraCSVDueDate and linearDueDate already draw the line in the
// same place; three transports agreeing is what makes it the package's rule rather than this
// file's preference.
//
// A value that ARRIVED AND WAS REFUSED is the opposite case and IS reported. It has to be: a due
// date dropped for being unparseable and an issue with no due date are the same NULL column, so the
// note is the only thing that distinguishes "Track read your due dates" from "Track read some of
// them". That is the same argument csv.go's FieldNote block makes, and the leaked header row in the
// corpus is a real population that exercises it.
//
// It parses with parseLinearCSVTime — THIS transport's layouts — and deliberately not with
// parseJiraCSVTime. The two lists are kept apart in linear_csv_dates.go on measured grounds (one
// provider renders the same instant two completely different ways on its API and in its CSV), and
// reusing the Jira parser here would lend a Linear export the evidence a Jira measurement gathered.
func linearCSVDueDate(raw string) (*time.Time, []FieldNote) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	t, ok := parseLinearCSVTime(raw)
	if !ok {
		return nil, []FieldNote{{Field: fieldDueDate, Value: raw, Via: viaUnparseableDate}}
	}
	return &t, nil
}
