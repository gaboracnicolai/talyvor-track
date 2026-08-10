package importer

import (
	"testing"
	"time"
)

// linear_csv_tostring_dates_test.go — the unit half. The end-to-end half is in
// linear_csv_tostring_dates_job_test.go; this pins the parse itself against VERBATIM CORPUS BYTES
// and pins the two properties the fix must not trade away.

// The cells below are copied byte for byte out of the corpus extract
// (/tmp/w34-linear-csv-date-cells.txt, produced by scripts/w34-linear-csv-updated-probe.py). They
// are LITERALS rather than values formatted with linearCSVDateToStringLayout: a fixture built from
// the constant the code parses with compares the constant to itself and passes for every possible
// value, including a wrong one.
const (
	corpusCellGMTName    = "Fri Feb 06 2026 10:01:29 GMT+0000 (GMT)"       // wubin28, kkoocheki, AlexanderJson, JocoBorghol
	corpusCellOffsetName = "Fri Apr 17 2026 04:00:00 GMT+0000 (GMT+00:00)" // gong8, kapishdima
	corpusCellISOMillis  = "2024-09-05T04:59:25.361Z"                      // 8 owners — must keep parsing
	corpusCellISOSeconds = "2026-06-15T15:03:40Z"                          // madhura68 — must keep parsing
)

func TestParseLinearCSVTime_TheTwoZoneNameSpellingsTheCorpusCarries(t *testing.T) {
	for _, tc := range []struct {
		name, cell string
		want       time.Time
	}{
		{"(GMT) — 454 cells, 4 owners", corpusCellGMTName,
			time.Date(2026, 2, 6, 10, 1, 29, 0, time.UTC)},
		{"(GMT+00:00) — 292 cells, 2 owners", corpusCellOffsetName,
			time.Date(2026, 4, 17, 4, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLinearCSVTime(tc.cell)
			if !ok {
				t.Fatalf("parseLinearCSVTime(%q) refused the cell — this is the shape 746 of 2,947 "+
					"real `Updated` cells carry; refusing it defaults the column to NOW()", tc.cell)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseLinearCSVTime(%q) = %s, want %s",
					tc.cell, got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

// TestParseLinearCSVTime_TheShapesThatAlreadyParsedStillDo is the regression half. The strip runs
// on EVERY value now, so the two layouts that were already carrying 4,698 of 5,440 distinct corpus
// cells have to be re-asserted rather than assumed.
func TestParseLinearCSVTime_TheShapesThatAlreadyParsedStillDo(t *testing.T) {
	for _, tc := range []struct {
		cell string
		want time.Time
	}{
		{corpusCellISOMillis, time.Date(2024, 9, 5, 4, 59, 25, 361000000, time.UTC)},
		{corpusCellISOSeconds, time.Date(2026, 6, 15, 15, 3, 40, 0, time.UTC)},
		{"2026-01-15", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)},
	} {
		got, ok := parseLinearCSVTime(tc.cell)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("parseLinearCSVTime(%q) = (%s, %v), want (%s, true)",
				tc.cell, got.Format(time.RFC3339Nano), ok, tc.want.Format(time.RFC3339Nano))
		}
	}
}

// TestParseLinearCSVTime_ANonZeroOffsetIsHonoured covers the half the corpus does NOT exercise —
// 734 of 734 real toString cells are GMT+0000, so the offset arithmetic is a property of the
// layout here and is called that rather than presented as a measurement.
func TestParseLinearCSVTime_ANonZeroOffsetIsHonoured(t *testing.T) {
	got, ok := parseLinearCSVTime("Fri Feb 06 2026 10:01:29 GMT+0530 (India Standard Time)")
	if !ok {
		t.Fatal("refused a toString cell with a non-zero offset and a multi-word zone name")
	}
	want := time.Date(2026, 2, 6, 4, 31, 29, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s — the NUMERIC offset is the authoritative part; the name is not",
			got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// TestParseLinearCSVTime_TheStripIsAnchoredToTheOffset is the narrowing guard. Every value here
// must STILL be refused: the fix widens exactly one shape, and linear_csv_dates.go's whole argument
// is that the refusal — not the layout list — is what keeps a hand-pinned parser honest.
func TestParseLinearCSVTime_TheStripIsAnchoredToTheOffset(t *testing.T) {
	for _, cell := range []string{
		"15/01/2026 10:23",       // the shape the job fixture's third row carries
		"2026-01-15 (approx)",    // a trailing parenthetical NOT preceded by an offset
		"2026-01-15 (GMT)",       // ... even when the parenthetical looks like a zone name
		"Jan 15 2026 (GMT+0000)", // ... even when the offset is INSIDE the parenthetical
		"23/Jul/2026 7:36 PM",    // the JIRA CSV shape — a different provider's list, deliberately not shared
	} {
		if got, ok := parseLinearCSVTime(cell); ok {
			t.Errorf("parseLinearCSVTime(%q) = %s, want refused — a value this parser cannot read "+
				"must reach the operator as a warning, not as a guessed timestamp",
				cell, got.Format(time.RFC3339))
		}
	}
}

// TestParseLinearCSVTime_TheMSTLayoutWouldHaveCoveredOnlyFourOfSixOwners MEASURES the claim
// linear_csv_tostring_dates.go's comment makes about the road not taken, instead of asserting it in
// prose. A single layout ending `GMT-0700 (MST)` is the obvious one-line fix; Go's MST verb reads a
// zone ABBREVIATION, so it accepts the corpus's " (GMT)" spelling and refuses its " (GMT+00:00)"
// one — 454 cells from four owners fixed, 292 cells from the other two still defaulting, and every
// test in this package green either way.
//
// ⚠ IT ASSERTS BOTH DIRECTIONS. The accept half is what makes the refuse half a real asymmetry
// rather than a layout that simply does not work.
func TestParseLinearCSVTime_TheMSTLayoutWouldHaveCoveredOnlyFourOfSixOwners(t *testing.T) {
	const mstLayout = "Mon Jan 02 2006 15:04:05 GMT-0700 (MST)"

	if _, err := time.Parse(mstLayout, corpusCellGMTName); err != nil {
		t.Errorf("the MST layout REFUSED %q (%v) — the comment claims it accepts this spelling",
			corpusCellGMTName, err)
	}
	if _, err := time.Parse(mstLayout, corpusCellOffsetName); err == nil {
		t.Errorf("the MST layout ACCEPTED %q — the comment claims it cannot, and the whole reason "+
			"the fix strips the name instead of pinning a fourth layout is that it cannot",
			corpusCellOffsetName)
	}
}

// TestStripJSDateToStringZoneName_LeavesEverythingElseByteIdentical is the property the strip is
// allowed to have and no other: a value without the anchored tail comes back unchanged, byte for
// byte, so no existing shape can reach a layout by a route it could not reach before.
func TestStripJSDateToStringZoneName_LeavesEverythingElseByteIdentical(t *testing.T) {
	for _, s := range []string{
		corpusCellISOMillis,
		corpusCellISOSeconds,
		"2026-01-15",
		"2026-01-15 (approx)",
		"Fri Feb 06 2026 10:01:29 GMT+0000", // already bare — nothing to strip
		"",
	} {
		if got := stripJSDateToStringZoneName(s); got != s {
			t.Errorf("stripJSDateToStringZoneName(%q) = %q, want it unchanged", s, got)
		}
	}
	// And it DOES fire on the two shapes it exists for, keeping the offset.
	for cell, want := range map[string]string{
		corpusCellGMTName:    "Fri Feb 06 2026 10:01:29 GMT+0000",
		corpusCellOffsetName: "Fri Apr 17 2026 04:00:00 GMT+0000",
	} {
		if got := stripJSDateToStringZoneName(cell); got != want {
			t.Errorf("stripJSDateToStringZoneName(%q) = %q, want %q", cell, got, want)
		}
	}
}
