package importer

import "regexp"

// linear_csv_tostring_dates.go — the third shape a real Linear CSV export's date columns carry, and
// the one that made #89's and #100's merges inert on a quarter of the corpus.
//
// ⚠⚠ THE COLUMN WAS THE FINDING TWICE AND THE PARSER IS THE FINDING HERE. #89 taught
// linearRowMapper to read `Created`/`Completed` and #100 taught it `Updated`; both land the cell on
// parseLinearCSVTime, whose pinned list was TWO layouts. MEASURED by
// scripts/w34-linear-csv-updated-probe.py over 45 real Linear CSV exports that unrelated tenants
// committed to public repositories (three negative controls first — a fabricated column set must
// find 0 files, a fabricated repository and a fabricated path must both refuse), re-run at
// b45a39b:
//
//	Created  2,990 non-empty cells  ISO+ms 2,195 · toString 746 (24.9%) · ISO 43 · header leak 6
//	Updated  2,947 non-empty cells  ISO+ms 2,195 · toString 746 (25.3%) ·           header leak 6
//
// Applied to the 5,440 DISTINCT cells the probe extracted, the SHIPPED parseLinearCSVTime accepted
// 4,698 and refused 742 — 734 toString cells plus 8 header rows that leaked into the data. So on a
// quarter of real rows the date is refused, the column takes its `TIMESTAMPTZ DEFAULT NOW()`, and
// the issue records as opened and last touched AT IMPORT TIME: exactly the loss those two merges
// were written to stop, arriving through the parser instead of through the mapper.
//
// ⚠ THE PROVENANCE IS SIX OWNERS WHO HAVE NEVER MET, which is the only thing that turns second-hand
// bytes into evidence:
//
//	" (GMT)"        454 cells · 4 owners (AlexanderJson, JocoBorghol, kkoocheki, wubin28) · widths 30, 34
//	" (GMT+00:00)"  292 cells · 2 owners (gong8, kapishdima)                               · width 34
//
// against 8 owners across widths 29/30/34 for the ISO shape. Two independent zone-name spellings
// from two disjoint owner sets is also why the fix reads the parenthetical GENERICALLY rather than
// pinning either literal.
//
// ⚠ WHAT IS NOT CLAIMED, said rather than implied: that Linear's own web export emits this. All 734
// distinct toString cells carry offset GMT+0000 and NOT ONE omits the parenthetical, which is what
// a script that read Linear's API and re-serialised through a JS `Date` under TZ=UTC would produce.
// It is still a file a user HAS and uploads, and it is a Linear export by every structural test the
// probe applies (the corpus is selected on Linear's own `Cycle Number`/`Cycle Name`/`Triaged`/
// `Canceled` header markers). What justifies READING it is not who wrote it but that the instant is
// unambiguous — see the layout's comment.
//
// ⚠ AND THE OFFSET PATH IS EXERCISED BY NO REAL CELL: 734 of 734 are GMT+0000. The layout reads
// whatever offset arrives and TestParseLinearCSVTime_ANonZeroOffsetIsHonoured covers one, but the
// corpus does not, so that half is a property of the layout rather than a measurement.

// linearCSVDateToStringLayout is ECMAScript's Date.prototype.toString shape with the trailing zone
// NAME removed (see jsDateToStringZoneName). ECMA-262 renders that value as the date, the time, a
// NUMERIC UTC offset, and then an OPTIONAL implementation-defined zone name in parentheses. The
// offset is the authoritative part and the name carries nothing it does not — which is the whole
// argument for accepting this shape rather than refusing it: the instant is not being guessed at.
//
// ⚠ IT IS NOT `... GMT-0700 (MST)` AS ONE LAYOUT, AND THAT WAS MEASURED RATHER THAN PREFERRED. Go's
// MST verb reads a zone ABBREVIATION, so it accepts the corpus's " (GMT)" spelling and REFUSES its
// " (GMT+00:00)" one — which would have fixed 454 cells from four owners and left 292 cells from
// the other two still defaulting, with every test in this package green. Stripping the name and
// reading the offset covers both spellings and the third one nobody here has seen
// (" (Eastern European Summer Time)").
const linearCSVDateToStringLayout = "Mon Jan 02 2006 15:04:05 GMT-0700"

// jsDateToStringZoneName matches ONLY a trailing parenthetical that directly follows a numeric GMT
// offset, and captures the offset so the strip keeps it.
//
// ⚠ THE ANCHOR AND THE `GMT[+-]\d{4}` PREFIX ARE THE LOAD-BEARING PART, not tidiness. A rule that
// removed ANY trailing parenthetical would change what this package does with values it refuses
// today — `2026-01-15 (approx)` would become a date — and linear_csv_dates.go's argument is that the
// REFUSAL is the load-bearing behaviour, not the layout list: a tenant whose serialisation matches
// nothing must learn it in the warnings channel instead of receiving a column of import-instant
// timestamps that reads as a working import. This narrows nothing and widens exactly one shape.
// TestJobRow_LinearCSV_AnUnknownDateShapeIsStillRefusedAndReported is what says so.
var jsDateToStringZoneName = regexp.MustCompile(`( GMT[+-]\d{4}) \([^)]*\)$`)

// stripJSDateToStringZoneName drops the optional zone name, leaving the numeric offset in place.
// A value that does not carry one is returned unchanged, byte for byte.
func stripJSDateToStringZoneName(s string) string {
	return jsDateToStringZoneName.ReplaceAllString(s, "$1")
}
