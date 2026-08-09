package importer

import (
	"strings"
	"testing"
)

// jira_csv_labels_test.go — a real Jira CSV export repeats a MULTI-VALUE column once per value, and
// buildIndex is a map, so fifteen `Labels` columns collapse to one and the importer keeps whichever
// value happened to land in the last of them.
//
// ⚠ THE FACT WAS ALREADY MEASURED AND WRITTEN DOWN IN THIS PACKAGE. jira_csv_dates.go's header says,
// as its reason for not pinning a column count: "an all-fields export repeats multi-value columns per
// row, so the same view answered 212 and 279 for two different result sets". #78 observed the
// repetition, recorded it, and asked what it meant for the COLUMN COUNT — nobody asked what it meant
// for `Labels`, which the mapper three lines away had been reading since the first CSV merge.
//
// MEASURED 2026-08-09 against a real Jira CSV export (jira.atlassian.com, anonymous, the
// issue-navigator "csv-all-fields" view). NEGATIVE-CONTROLLED FIRST so a 200 is not a blanket answer:
// fabricated host ⇒ no resolution (URLError) · fabricated VIEW on the real host ⇒ 400 text/html ·
// fabricated PROJECT in the JQL ⇒ 400 text/html. Only the real request answered 200 text/csv.
// scripts/w34-jira-csv-labels-probe.py re-runs the whole measurement.
//
//	THREE RESULT SETS FROM THE SAME VIEW, three different widths:
//	  labels IS NOT EMPTY, 6 issues   ⇒ 257 columns, 15 × "Labels"   (also 19 × "Comment")
//	  labels IS NOT EMPTY, 3 issues   ⇒ 224 columns,  2 × "Labels"
//	  no label predicate, 5 issues    ⇒ 212 columns,  1 × "Labels"
//
// ⚠ THE COUNT IS THE WIDEST ROW IN THE RESULT SET, WHICH IS WHAT MAKES THE FAILURE GENERAL RATHER
// THAN ODD: every issue gets as many `Labels` columns as the most-labelled issue in the export, and
// the rest are padded EMPTY. So the mapper reads the last column and an issue with fewer labels than
// the widest row imports NONE AT ALL. Driven over the 6-issue export: 25 label values present, ONE
// imported, and FIVE of the six issues imported zero while carrying two each. The importer reported
// {imported:6, skipped:0, warnings:[]} — this item's "data loss reported as success" shape, EIGHTH
// instance, in the one transport a customer can run without credentials.
//
// ⚠ AND THE ASYMMETRY IS THE ARGUMENT: jira_api reads `labels` as a JSON array and keeps every one of
// them (jira.go:175), as does linear_api (linear.go:267). The same field, the same product, two
// transports — and only the credential-free one loses the data. That is #78's priority note arriving
// as a defect: "before taking another API field, ask whether the CSV side of the same field is the
// bigger hole".
//
// The fixtures below are the REAL bytes of two real issues, named so a reader can tell a measured
// value from an invented one at the call site.

// JRASERVER-79469 — two labels, in the FIRST two of fifteen columns. The majority shape.
var realJiraCSVLabelsNarrowRow = []string{"whl-fy27q1", "whl-fy27q1-20"}

// JRASERVER-79446 — the widest issue in that export, and the reason the other five rows are padded
// out to fifteen columns.
var realJiraCSVLabelsWidestRow = []string{
	"2.4.3", "accessibility", "ax-a11y--1728", "ax-at-user", "ax-bug", "ax-customer-escalated",
	"ax-eap-support", "ax-esc-JPMC", "ax-high-priority", "ax-kb-user", "ax-qa", "ax-qa-fixed",
	"ax-qa-verified", "Level-A", "WCAG2.2",
}

// realJiraCSVLabelsHeader is the measured header, verbatim, from column 30 to column 49 of the
// 6-issue export — the `Labels` run with its real neighbours on both sides. `Component/s` is there
// because it is ALSO a repeated multi-value column (2 of them) and `Due Date` because it is the
// single-occurrence column #78 reads, so a fix that confused the two would be visible here.
//
// The spellings are HARDCODED, never read from a constant the mapper also reads — #75's C6: a guard
// that compares the constant to itself passes for every possible value.
func realJiraCSVLabelsHeader(labelCols int) []string {
	h := []string{"Component/s", "Component/s", "Due Date", "Votes"}
	for i := 0; i < labelCols; i++ {
		h = append(h, "Labels")
	}
	return append(h, "Description", "Summary", "Status", "Priority")
}

// rowFor pads a label list out to the export's width the way Jira does — values first, then empties.
func rowFor(labels []string, labelCols int, tail ...string) []string {
	row := []string{"", "", "", ""}
	for i := 0; i < labelCols; i++ {
		if i < len(labels) {
			row = append(row, labels[i])
		} else {
			row = append(row, "")
		}
	}
	return append(row, tail...)
}

// ─── the defect ─────────────────────────────────────────────

// The measured majority case: an issue whose labels sit in the first columns of a wider export.
// Today buildIndex resolves "labels" to the LAST of the fifteen columns, which is empty, so a row
// carrying two labels imports none.
func TestJiraCSVLabels_AnIssueNarrowerThanTheExportKeepsItsLabels(t *testing.T) {
	const labelCols = 15 // the measured width of the 6-issue export
	got := mapOneJiraCSVRow(t,
		realJiraCSVLabelsHeader(labelCols),
		rowFor(realJiraCSVLabelsNarrowRow, labelCols, "a description", "Atlassian Diagnostics plugin", "Closed", "Medium"))

	assertLabels(t, got.issue.Labels, realJiraCSVLabelsNarrowRow)
}

// The widest row: every one of its fifteen labels must land, not just whichever the last column holds.
func TestJiraCSVLabels_TheWidestRowKeepsEveryLabel(t *testing.T) {
	const labelCols = 15
	got := mapOneJiraCSVRow(t,
		realJiraCSVLabelsHeader(labelCols),
		rowFor(realJiraCSVLabelsWidestRow, labelCols, "a description", "Accessibility bug", "Closed", "Medium"))

	assertLabels(t, got.issue.Labels, realJiraCSVLabelsWidestRow)
}

// The two-column export measured in the same run — a narrower repetition, so the fix cannot be
// special-cased to fifteen.
func TestJiraCSVLabels_TwoColumnsAreBothRead(t *testing.T) {
	const labelCols = 2
	got := mapOneJiraCSVRow(t,
		realJiraCSVLabelsHeader(labelCols),
		rowFor(realJiraCSVLabelsNarrowRow, labelCols, "a description", "Ship it", "Closed", "Medium"))

	assertLabels(t, got.issue.Labels, realJiraCSVLabelsNarrowRow)
}

// ─── the shared index, asked directly ───────────────────────
//
// A different catcher for a different failure mode (the #100 lesson): the two tests above go through
// the mapper, so a mapper that hardcoded a column list could satisfy them. This one asks the INDEX
// whether it can still see a repeated header at all.
func TestColumnIndex_GetNamesTheFirstOccurrenceNotTheLast(t *testing.T) {
	ci := buildIndex([]string{"Summary", "Labels", "Labels", "Labels", "Status"})
	row := []string{"Ship it", "alpha", "", "gamma", "Closed"}

	// The single-value accessor keeps naming ONE column, and it must be the FIRST. Last-occurrence
	// was never a decision — it is `out[h] = i` overwriting in header order, and that accident is
	// the whole defect: on the measured export the last of fifteen columns is the empty one.
	if got := ci.get(row, "Labels"); got != "alpha" {
		t.Errorf("get(Labels) = %q, want %q (the first column of that name)", got, "alpha")
	}
	// The single-occurrence case, which is every column the mappers actually read, is unaffected in
	// either direction — this is the half that must stay green before AND after.
	if got := ci.get(row, "Summary"); got != "Ship it" {
		t.Errorf("get(Summary) = %q, want %q", got, "Ship it")
	}
	if got := ci.get(row, "Nonexistent"); got != "" {
		t.Errorf("get(absent) = %q, want empty", got)
	}
}

// ⚠ THIS ONE WAS NOT RED FIRST AND THAT IS STATED RATHER THAN IMPLIED: it names an accessor that did
// not exist, so in a compiled language it could only be written after the fix — the five behavioural
// tests above are what demonstrated the defect. It earns its place as a SEPARATE catcher (a mapper
// that special-cased "Labels" would satisfy those five and fail here), and because it could not be
// red-first it is the control campaign that shows it can fail: C4 blinds getAll and reds it.
func TestColumnIndex_GetAllReturnsEveryOccurrenceInHeaderOrder(t *testing.T) {
	ci := buildIndex([]string{"Summary", "Labels", "Labels", "Labels", "Status"})

	// Empties are the padding an export writes on a row narrower than the widest; they are dropped,
	// and the surviving values keep header order.
	assertLabels(t, ci.getAll([]string{"Ship it", "alpha", "", "gamma", "Closed"}, "Labels"),
		[]string{"alpha", "gamma"})

	// A short row must not panic — csvSource rejects ragged rows, but getAll is index arithmetic over
	// caller data and is not entitled to assume that.
	assertLabels(t, ci.getAll([]string{"Ship it", "alpha"}, "Labels"), []string{"alpha"})

	// Absent and all-empty are both the empty, non-nil slice.
	assertLabels(t, ci.getAll([]string{"Ship it", "", "", "", "Closed"}, "Labels"), []string{})
	assertLabels(t, ci.getAll([]string{"Ship it"}, "Nonexistent"), []string{})
}

// ─── the floors: what must NOT change ───────────────────────

// The single comma-joined column is the shape csv_test.go's TestImportLinearCSV_ParsesLabelsAsArray
// has pinned since the first CSV merge, and it is the only shape this environment has ever measured
// for a Linear export. With one column the new path must be byte-identical to the old one.
func TestJiraCSVLabels_ASingleCommaJoinedColumnIsUnchanged(t *testing.T) {
	got := mapOneJiraCSVRow(t,
		[]string{"Summary", "Status", "Priority", "Labels"},
		[]string{"Ship it", "Closed", "Medium", "alpha, beta ,gamma"})

	assertLabels(t, got.issue.Labels, []string{"alpha", "beta", "gamma"})
}

// An absent column and an empty one both stay an empty, NON-NIL slice — downstream JSON encodes `[]`,
// which splitLabels' own comment promises and the API response shape depends on.
func TestJiraCSVLabels_AbsentAndEmptyStayEmptyNotNil(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header []string
		row    []string
	}{
		{"no Labels column at all", []string{"Summary", "Status"}, []string{"Ship it", "Closed"}},
		{"one empty Labels column", []string{"Summary", "Status", "Labels"}, []string{"Ship it", "Closed", ""}},
		{"three empty Labels columns", []string{"Summary", "Status", "Labels", "Labels", "Labels"}, []string{"Ship it", "Closed", "", "", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mapOneJiraCSVRow(t, tc.header, tc.row)
			if got.issue.Labels == nil {
				t.Fatalf("Labels is nil; splitLabels promises a non-nil empty slice so JSON encodes []")
			}
			if len(got.issue.Labels) != 0 {
				t.Errorf("Labels = %v, want empty", got.issue.Labels)
			}
		})
	}
}

// A repeated column must not disturb the SINGLE-occurrence columns beside it — `Due Date` sits two
// indices before the `Labels` run in the measured header, and #78's work reads it by name.
func TestJiraCSVLabels_TheRepetitionDoesNotDisturbTheDateColumn(t *testing.T) {
	const labelCols = 15
	header := realJiraCSVLabelsHeader(labelCols)
	row := rowFor(realJiraCSVLabelsNarrowRow, labelCols, "a description", "Ship it", "Closed", "Medium")
	row[2] = realJiraCSVDueDate // the "Due Date" column, at its measured index

	got := mapOneJiraCSVRow(t, header, row)
	if got.issue.DueDate == nil {
		t.Fatalf("DueDate is nil; the measured export puts %q two columns before the Labels run", realJiraCSVDueDate)
	}
	if want := wantUTC(t, "2025-01-19T00:00:00Z"); !got.issue.DueDate.Equal(want) {
		t.Errorf("DueDate = %s, want %s", got.issue.DueDate.UTC(), want)
	}
}

// ─── the Linear half, and exactly what it does and does not claim ───
//
// The collapse is a property of buildIndex, which BOTH CSV mappers share, so the fix lands in the
// index and linearRowMapper inherits it. ⚠ THIS IS NOT A CLAIM ABOUT LINEAR'S EXPORT. Linear's CSV is
// produced in-app behind authentication and nothing in this environment can fetch one, so whether it
// repeats a multi-value column is UNMEASURED — the same gap
// TestLinearCSVMapper_StillReadsNoDates_AKnownUnmeasuredGap pins for the date columns. What this
// asserts is only that the two transports cannot drift: if a Linear export does repeat, it is read
// the same way, and if it does not, the single-column behaviour is unchanged.
func TestLinearCSVLabels_TheSharedIndexTreatsBothTransportsAlike(t *testing.T) {
	ci := buildIndex([]string{"Title", "Status", "Priority", "Labels", "Labels"})
	got, err := linearRowMapper(ci, []string{"Ship it", "Done", "High", "alpha", "beta"})
	if err != nil {
		t.Fatalf("linearRowMapper: %v", err)
	}
	assertLabels(t, got.issue.Labels, []string{"alpha", "beta"})

	// And the single-column shape, which is the only one ever measured for Linear, is untouched.
	ci = buildIndex([]string{"Title", "Status", "Priority", "Labels"})
	got, err = linearRowMapper(ci, []string{"Ship it", "Done", "High", "alpha, beta"})
	if err != nil {
		t.Fatalf("linearRowMapper: %v", err)
	}
	assertLabels(t, got.issue.Labels, []string{"alpha", "beta"})
}

// assertLabels compares in ORDER — a set comparison would pass a fix that reads the columns backwards.
func assertLabels(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("labels = %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labels[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}
