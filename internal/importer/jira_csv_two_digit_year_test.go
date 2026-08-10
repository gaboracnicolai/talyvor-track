package importer

import (
	"testing"
	"time"
)

// jira_csv_two_digit_year_test.go — the CI-runnable half. The corpus lives in /tmp on one machine
// and jira_csv_corpus_census_test.go SKIPS without it; a finding whose only guard skips in CI is
// not guarded. Every literal below is a VERBATIM cell lifted out of a real export in that corpus,
// hardcoded here so the assertion survives the corpus being absent.

// realJiraCSVTwoDigitYearCells are (cell, want) pairs. The want instants are written out by hand —
// NOT produced by formatting through the layout under test, which would compare the constant to
// itself and pass for every possible value (#75's C6).
var realJiraCSVTwoDigitYearCells = []struct {
	cell string
	want time.Time
}{
	// `2/Jan/06 3:04 PM` — 27,147 cells, the commonest shape in the corpus.
	{"24/Jun/26 5:39 PM", time.Date(2026, 6, 24, 17, 39, 0, 0, time.UTC)},
	{"05/Jun/26 11:20 PM", time.Date(2026, 6, 5, 23, 20, 0, 0, time.UTC)},
	{"7/Mar/24 4:27 PM", time.Date(2024, 3, 7, 16, 27, 0, 0, time.UTC)},
	// `2/Jan/06 15:04` — 2,133 cells.
	{"29/Jan/16 02:53", time.Date(2016, 1, 29, 2, 53, 0, 0, time.UTC)},
	// `2006-01-02 15:04` — 1,228 cells.
	{"2025-07-04 16:15", time.Date(2025, 7, 4, 16, 15, 0, 0, time.UTC)},
	{"2023-01-27 9:12", time.Date(2023, 1, 27, 9, 12, 0, 0, time.UTC)},
	// `2006-01-02 15:04:05` — 360 cells.
	{"2024-05-27 03:47:00", time.Date(2024, 5, 27, 3, 47, 0, 0, time.UTC)},
	// `Jan/2/06 3:04 PM` — 212 cells.
	{"Nov/26/25 12:26 AM", time.Date(2025, 11, 26, 0, 26, 0, 0, time.UTC)},
	{"Mar/11/25 2:17 PM", time.Date(2025, 3, 11, 14, 17, 0, 0, time.UTC)},
	// `2/Jan/06` — 10 cells, Due Date only.
	{"11/May/24", time.Date(2024, 5, 11, 0, 0, 0, 0, time.UTC)},
	// The shape that was already pinned, kept so a reordering that breaks it is caught here.
	{"23/Jul/2026 7:36 PM", time.Date(2026, 7, 23, 19, 36, 0, 0, time.UTC)},
}

func TestJiraCSVTime_AcceptsTheShapesRealExportsActuallyEmit(t *testing.T) {
	for _, c := range realJiraCSVTwoDigitYearCells {
		got, ok := parseJiraCSVTime(c.cell)
		if !ok {
			t.Errorf("parseJiraCSVTime(%q) REFUSED — this cell is verbatim from a real export; "+
				"see jira_csv_two_digit_year.go for the census", c.cell)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parseJiraCSVTime(%q) = %s, want %s",
				c.cell, got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}

// ⚠ THE HALF THAT MATTERS MORE THAN THE ACCEPTANCES. A date parser is dangerous in the direction of
// LENIENCE: the shapes below are the ambiguous remainder, and if a future edit makes any of them
// parse then roughly half of that shape's cells have been silently moved to a wrong-but-plausible
// instant. Refusing them is the behaviour under test, not a gap in it.
func TestJiraCSVTime_StillRefusesTheAmbiguousDayMonthShapes(t *testing.T) {
	for _, cell := range []string{
		"12-12-2024 14:42", // dd-mm or mm-dd — undecidable
		"22/08/2024",       // d/m here, but the format class is not determined
		"7/9/2026 10:00",   // undecidable
		"7/15/2026 20:53",  // m/d here
		"5/28/25 17:00",    // m/d here
		"10/15/2020 21:06",
		"29-08-24 20:38",
		"12/18/24 10:13",
	} {
		if got, ok := parseJiraCSVTime(cell); ok {
			t.Errorf("parseJiraCSVTime(%q) = %s — an ambiguous day/month order MUST stay refused. "+
				"A refused cell is reported in the warnings channel; a mis-parsed one is reported "+
				"nowhere and cannot be told apart afterwards.", cell, got.Format(time.RFC3339))
		}
	}
}
