package importer

import (
	"strings"
	"testing"
	"time"
)

// linear_csv_due_date_test.go — the guards behind linear_csv_due_date.go.
//
// ⚠ THIS COLUMN WAS PINNED SHUT ON PURPOSE, AND THIS FILE IS THE CONDITION THAT PIN NAMED.
// TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers (jira_csv_dates_test.go) asserted
// `DueDate == nil` and gave its reason in full: "the documented export HAS NO DUE-DATE COLUMN AT
// ALL … a due date read out of some neighbouring column would be invented … Whoever gets a real
// Linear export re-measures the layouts; nobody should make this mapper grow a due date without
// one." That was the right call on the evidence it had. The evidence has changed, in exactly the
// way that test said would change it, and the reason it rested on is now measurably false:
//
// the word "documented" meant Linear's IMPORT documentation — the nine columns
// Title · Description · Priority · Status · Assignee · Created · Completed · Labels · Estimate.
// That header has no due date in it. Linear's EXPORT header does, in every real file measured.
// This is #86's finding a second time: THE DOCUMENTED IMPORT HEADER IS NOT THE EXPORT HEADER, and
// reading the columns off the wrong one is how a field stays invisible.
//
// Provenance for every literal below: scripts/w34-linear-csv-column-census-probe.py, run
// 2026-08-10 over the public-repository corpus #99 opened, negative-controlled first (fabricated
// column set ⇒ 0 files · fabricated repository ⇒ REFUSED · fabricated path in a real repository ⇒
// REFUSED · a FABRICATED COLUMN NAME ⇒ 0 headers / 0 cells, which is the control the three earlier
// probes do not have and the only one that proves the census can report a zero at all):
//
//	45 files · 17 distinct owners · 3,099 data rows · 6 header shapes (29/30/34 cols)
//	`Due Date` in header       45 of 45 files, ALL SIX header shapes, all 17 owners
//	`Due Date` non-empty       447 of 3,099 rows (14.4%)
//
// Second-hand bytes, and this file says so rather than borrowing the Jira probe's first-hand
// provenance — #75's overclaim, not repeated here.

// ─── rule 2: the measured bytes ────────────────────────────────────────────
//
// The cell strings below are REAL CELLS, transcribed from the corpus by hand and not generated,
// because a fixture generated from the parser's own layout list agrees with that list for every
// possible value. The serialisation census is the whole reason no new layout is added by this
// merge — 441 of the 447 real cells are in two shapes parseLinearCSVTime ALREADY accepts:
//
//	408  9999-99-99A99:99:99.999A                        owners=3  UIT6, amo-tech-ai, ray-abhishek
//	 20  AAA AAA 99 9999 99:99:99 AAA+9999 (AAA+99:99)   owners=2  isakshay007, kapishdima
//	 13  AAA AAA 99 9999 99:99:99 AAA+9999 (AAA)         owners=1  kkoocheki
//	  6  AAA AAAA                                        owners=1  ray-abhishek  ← a leaked HEADER ROW
//
// Six unrelated owners across the two real shapes. The fourth line is the literal text "Due Date"
// sitting in a data row — the same leaked header the Status census counted — and it is a case, not
// noise: it MUST be refused and reported, never coerced into an instant.
func TestLinearCSVDueDate_RealExportCellsLandOnTheIssue(t *testing.T) {
	// The 34-column header shape, 30 of the 45 files — trimmed to the columns this test asserts on
	// plus `Due Date` in its real position relative to them.
	header := []string{"ID", "Title", "Status", "Priority", "Created", "Updated", "Due Date"}

	for _, tc := range []struct {
		name string
		cell string
		want time.Time
	}{{
		// 408 of 447 cells, 3 owners. ISO-8601 with MILLISECONDS and a `Z` — a shape
		// linearCSVTimeLayouts covers only because Go's time.Parse accepts a fractional second
		// the layout does not mention. That is a real property of the parser and not an
		// assumption: if it ever stopped holding, this case is what would say so.
		name: "ISO-8601 with milliseconds (408 cells, 3 owners)",
		cell: "2026-06-15T15:03:28.558Z",
		want: time.Date(2026, 6, 15, 15, 3, 28, 558000000, time.UTC),
	}, {
		// 20 cells, 2 owners. ECMAScript Date.prototype.toString with a NUMERIC zone name.
		name: "JS toString, numeric zone name (20 cells, 2 owners)",
		cell: "Sat May 09 2026 12:05:44 GMT+0000 (GMT+00:00)",
		want: time.Date(2026, 5, 9, 12, 5, 44, 0, time.UTC),
	}, {
		// 13 cells, 1 owner. The same shape with an ALPHABETIC zone name.
		name: "JS toString, alphabetic zone name (13 cells, 1 owner)",
		cell: "Sun May 11 2025 07:43:48 GMT+0000 (GMT)",
		want: time.Date(2025, 5, 11, 7, 43, 48, 0, time.UTC),
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := linearRowMapper(buildIndex(header),
				[]string{"ENG-1", "Ship it", "Done", "High", "2025-01-02", "2025-01-03", tc.cell})
			if err != nil {
				t.Fatal(err)
			}
			if got.issue.DueDate == nil {
				t.Fatalf("DueDate = nil for the real export cell %q — the column is in 45 of 45 "+
					"real Linear exports and this transport is the only one of the four that "+
					"drops it", tc.cell)
			}
			if !got.issue.DueDate.Equal(tc.want) {
				t.Errorf("DueDate = %v, want %v (from real cell %q)", got.issue.DueDate.UTC(), tc.want, tc.cell)
			}
			if n := notesFor(got.notes, fieldDueDate); len(n) != 0 {
				t.Errorf("a cell that parsed cleanly still reported %d note(s): %v — a clean import "+
					"must not report itself as degraded", len(n), n)
			}
		})
	}
}

// TestLinearCSVDueDate_TheLeakedHeaderRowIsRefusedAndReported — the fourth shape in the census.
//
// ⚠ THIS IS THE CASE THAT DECIDES THE FIX IS NOT A COERCION. A due date is NULLABLE, so a value
// silently dropped and a value never sent are the same empty column — the loss has no shape of its
// own. The only thing standing between "we read your due dates" and "we read some of them" is that
// a refused value is REPORTED, and this is the one real population that exercises it.
func TestLinearCSVDueDate_TheLeakedHeaderRowIsRefusedAndReported(t *testing.T) {
	header := []string{"Title", "Status", "Due Date"}
	got, err := linearRowMapper(buildIndex(header), []string{"Ship it", "Done", "Due Date"})
	if err != nil {
		t.Fatal(err)
	}
	if got.issue.DueDate != nil {
		t.Errorf("DueDate = %v for the literal cell \"Due Date\" — a leaked header row was coerced "+
			"into an instant", got.issue.DueDate)
	}
	n := notesFor(got.notes, fieldDueDate)
	if len(n) != 1 {
		t.Fatalf("a refused due date reported %d notes, want exactly 1 — an unparseable value that "+
			"reports nothing is indistinguishable from an issue with no due date", len(n))
	}
	if n[0].Via != viaUnparseableDate {
		t.Errorf("note.Via = %q, want %q", n[0].Via, viaUnparseableDate)
	}
	if n[0].Value != "Due Date" {
		t.Errorf("note.Value = %q — the note must carry the value it refused, or the operator "+
			"cannot tell which column shape broke", n[0].Value)
	}
}

// TestLinearCSVDueDate_AbsentIsNotReported — the asymmetry against Created/Updated, stated once.
//
// `created_at` and `updated_at` are DEFAULT NOW(), so an unsupplied value is a WRONG value that
// looks right, and their mappers report every absence. `due_date` is NULLABLE: an export with no
// due-date column, or with an empty cell, leaves a TRUTHFUL null. Reporting that as degradation
// would put a warning on every clean import of the 2,652 real rows (85.6%) whose Due Date cell is
// legitimately empty. Same rule jiraCSVDueDate and linearDueDate already follow — three transports
// agreeing is what makes it a rule rather than this file's preference.
func TestLinearCSVDueDate_AbsentIsNotReported(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header []string
		row    []string
	}{
		{"no Due Date column at all", []string{"Title", "Status"}, []string{"Ship it", "Done"}},
		{"the column exists, the cell is empty", []string{"Title", "Status", "Due Date"}, []string{"Ship it", "Done", ""}},
		{"the row is SHORT of the column", []string{"Title", "Status", "Due Date"}, []string{"Ship it", "Done"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := linearRowMapper(buildIndex(tc.header), tc.row)
			if err != nil {
				t.Fatal(err)
			}
			if got.issue.DueDate != nil {
				t.Errorf("DueDate = %v — invented from nothing", got.issue.DueDate)
			}
			if n := notesFor(got.notes, fieldDueDate); len(n) != 0 {
				t.Errorf("reported %d note(s) for an absent nullable due date: %v — that puts a "+
					"warning on 85.6%% of real rows", len(n), n)
			}
		})
	}
}

// TestLinearCSVDueDate_ANeighbouringDateColumnIsNotRead — THE GUARD THAT MOVED HERE, and the one
// thing this merge could have silently destroyed.
//
// TestLinearCSVMapper_ReadsTheDocumentedDatesAndInventsNoOthers asserted `DueDate == nil`, and its
// stated purpose was NOT only "Linear has no due date" — it was the Linear twin of
// TestJiraCSVColumns_ANeighbouringDateColumnIsNotRead: "a due date read out of some neighbouring
// column would be invented." Inverting that assertion satisfies the new behaviour and retires the
// old protection in the same edit, and nothing would have gone red. So it is re-established here
// against the real corpus, which supplies a much better decoy than the Jira side had to invent:
// every 34-column real export carries `Cycle Start`, `Cycle End`, `Started`, `Triaged`, `Canceled`
// and `Archived`, and `Cycle Start` renders in the SAME ISO-with-milliseconds shape as `Due Date`
// (632 cells, 6 owners). A mapper reading position instead of name, or matching on a substring,
// lands on one of these.
func TestLinearCSVDueDate_ANeighbouringDateColumnIsNotRead(t *testing.T) {
	// Real neighbours, real serialisation, and NO `Due Date` column at all.
	header := []string{"Title", "Status", "Cycle Start", "Cycle End", "Started", "Archived"}
	row := []string{"Ship it", "Done",
		"2026-06-08T05:00:00.000Z", "2026-06-22T05:00:00.000Z",
		"Sun May 11 2025 08:15:24 GMT+0000 (GMT)", "Mon Apr 13 2026 07:50:20 GMT+0000 (GMT+00:00)"}
	got, err := linearRowMapper(buildIndex(header), row)
	if err != nil {
		t.Fatal(err)
	}
	if got.issue.DueDate != nil {
		t.Errorf("DueDate = %v — read out of a column that is not %q. Every value in this row is a "+
			"real cell from a real export, and none of them is a due date",
			got.issue.DueDate, linearCSVDueDateColumn)
	}
	if n := notesFor(got.notes, fieldDueDate); len(n) != 0 {
		t.Errorf("reported %d due-date note(s) for a row with no due-date column: %v", len(n), n)
	}
}

// ─── rule 1: the source claims ─────────────────────────────────────────────
//
// Scoped to linearRowMapper's OWN body: jiraRowMapper has assigned DueDate since #73, so a
// file-wide `strings.Contains(csv.go, "DueDate")` would pass on this repo whether or not the
// Linear half was ever wired. The subject of the claim is linearRowMapper and nothing else.
func TestLinearCSVDueDate_Rule1_TheMapperIsWiredIntoLinearRowMapper(t *testing.T) {
	body := identsOfFunc(t, "csv.go", "linearRowMapper")
	if !strings.Contains(body, "linearCSVDueDate") {
		t.Fatalf("linearRowMapper never calls linearCSVDueDate — the column is measured, mapped, " +
			"unit-tested and never read")
	}
	if !strings.Contains(body, "DueDate") {
		t.Errorf("linearRowMapper never assigns DueDate — the mapper can be called and its result " +
			"dropped on the floor")
	}
	// The positive control ON THIS GUARD: the function scoping must be real. If identsOfFunc
	// silently returned the whole file, jiraRowMapper's own jiraCSVDueDate call would satisfy the
	// assertions above and they would be measuring the wrong mapper.
	if strings.Contains(body, "jiraCSVDueDate") {
		t.Errorf("linearRowMapper's identifier set contains jiraCSVDueDate — the function scoping " +
			"is not working, so the two assertions above are answered by the wrong mapper")
	}
}

// notesFor returns every note for one field. Unlike renderOnly it does NOT require exactly one, so
// a test asserting a ZERO can use the same accessor as a test asserting a one.
func notesFor(notes []FieldNote, field string) []FieldNote {
	var out []FieldNote
	for _, n := range notes {
		if n.Field == field {
			out = append(out, n)
		}
	}
	return out
}
