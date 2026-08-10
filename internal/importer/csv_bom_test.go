package importer

// csv_bom_test.go — A REAL JIRA CSV EXPORT THAT BEGINS WITH A UTF-8 BOM IMPORTED NOTHING AT ALL,
// AND ONE FILE IN FIVE BEGINS WITH ONE.
//
// ⚠ THE THREE BYTES ARE EF BB BF AND THEY LAND ON THE FIRST HEADER CELL. Go's csv.Reader does not
// strip them (measured, TestCSVBOM_TheLanguageFactsThisRestsOn below) and neither does
// strings.TrimSpace, because unicode.IsSpace(U+FEFF) is FALSE. So buildIndex — which keys on
// strings.TrimSpace(strings.ToLower(h)) — files the first column under "\ufeffsummary", and
// jiraRowMapper's very first act:
//
//	title := ci.get(row, "Summary")        // misses: the key is "\ufeffsummary"
//	if title == "" { title = ci.get(row, "Title") }   // misses: there is no Title column
//	if title == "" { return mappedIssue{}, errEmptyTitle }
//
// refuses EVERY ROW OF THE FILE. Not a degraded import — a job that lands zero issues and reports
// {status:"failed", imported:0, skipped:0, failed:N}, which is #102's outcome exactly: the one
// result nobody re-runs and nobody investigates as a Track bug.
//
// ⚠ THE MEASUREMENT, whole-population (scripts/w34-jira-csv-corpus-probe.py, negative controls
// first) over REAL Jira CSV exports committed to PUBLIC repositories — the instrument #99 invented
// for the Linear half and that nobody had ever pointed at the Jira half:
//
//	304 files · 130 distinct owners · 17,921 data rows
//	 66/304 files (21.7%) begin with EF BB BF
//	 63 of those 66 carry it on `Summary`, the column the mapper reads for the title
//	 64/304 files (21.1%) have NO title column jiraRowMapper can find — 63 of them for this reason
//	 4,826/17,921 rows (26.9%) live in those files
//
//	first column, verbatim, across the corpus:
//	   227  'Summary'          63  '\ufeffSummary'   7  'Issue key'
//	     3  'Issue Type'        2  '\ufeffIssue Type'  1  '\ufeffClient Facing Time (in minutes)'
//	     1  'Project key'
//
// ⚠ THE PROPORTION THAT MATTERS IS PER FILE, NOT PER CORPUS — AN IMPORT IS ONE FILE. 21% reads like
// a minority; what it means is that one Jira export in five imports NOTHING, 100% of its rows, and
// the 64th file is the only honest refusal in the set (a filtered export carrying no title column
// at all, which errEmptyTitle is right to reject).
//
// ⚠⚠ THE SIBLING PROBE COULD NOT HAVE SEEN THIS AND ITS ANSWER IS STILL CORRECT.
// scripts/w34-jira-csv-export-probe.py drives a real Jira's own export endpoint and reads
// FIRST-HAND bytes — and that instance emits no BOM (re-measured this pass: the first 16 bytes of
// its export are `Summary,Issue ke`). One instance answering cleanly is not the population. Worse,
// that probe decodes with `body.decode("utf-8-sig")`, the codec a probe reaches for by reflex,
// which silently eats the byte: had that instance emitted a BOM the probe would still have printed
// a clean header. A LENIENT DECODER CANNOT SEE THE BYTE THAT BREAKS THE PRODUCT, so the corpus
// probe reads raw bytes and the BOM question is answered from `raw.startswith(b"\xef\xbb\xbf")`.
//
// ⚠ THE LINEAR HALF WAS ALREADY MEASURED AND IS CLEAN — 0 of 45 files, #102, also from raw bytes.
// That block wrote down what a BOM WOULD do to that transport ("buildIndex would key the first
// column '\ufeffid' and `ID` would go unread on every row — #99's duplication defect, silently
// reintroduced"). It was right about the mechanism and it measured the wrong provider: on Linear a
// BOM costs the routing key, on Jira it costs the TITLE, and only the second refuses the row.
// TestLinearCSV_ABOMdExportStillReadsTheRoutingKey holds Linear's half anyway — the fix is at the
// shared seam, so both transports are covered by construction and the enumeration is the point.

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"unicode"
)

// utf8BOMLiteral is the byte sequence, written as an escape so THIS FILE does not itself begin
// with one (the Go compiler rejects a BOM anywhere but the first byte of a source file, which is
// its own small confirmation that these three bytes are a file-level marker and not text).
const utf8BOMLiteral = "\uFEFF"

// jiraCSVBOMdExport is a real BOM'd export's first two lines, VERBATIM, from the measured
// population: a 21-column Jira Cloud export whose first column is `Summary`. Nothing is edited —
// the leading BOM, the column spellings, the `6/21/2025 16:14` date shape and the values are the
// bytes that repository holds.
//
// ⚠ THE FIRST THREE BYTES ARE THE FIXTURE. Written as utf8BOMLiteral + the rest rather than pasted,
// because a BOM pasted into a source file is invisible in every diff and every review, and a
// fixture whose defining byte can be silently lost in an editor is a fixture that quietly stops
// testing anything.
const jiraCSVBOMdExport = utf8BOMLiteral +
	"Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Priority,Reporter,Created,Updated\n" +
	"Agregar Producto,QUAS-1,10000,Task,To Do,QUAS,Quantum-Stock,software,Medium,Romario Isaias Abreu Santos,6/21/2025 16:14,6/21/2025 17:13\n" +
	"Desarrollo del Backend,QUAS-3,10002,Epic,To Do,QUAS,Quantum-Stock,software,Medium,Romario Isaias Abreu Santos,6/21/2025 16:15,6/21/2025 16:21\n"

// jiraCSVNoBOMExport is the SAME two issues without the three bytes — the shape 238 of the 304
// measured files use. It is the must-stay-green companion: whatever this merge does to the BOM'd
// shape, the ordinary shape has to keep importing exactly as it always has.
const jiraCSVNoBOMExport = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Priority,Reporter,Created,Updated\n" +
	"Agregar Producto,QUAS-1,10000,Task,To Do,QUAS,Quantum-Stock,software,Medium,Romario Isaias Abreu Santos,6/21/2025 16:14,6/21/2025 17:13\n" +
	"Desarrollo del Backend,QUAS-3,10002,Epic,To Do,QUAS,Quantum-Stock,software,Medium,Romario Isaias Abreu Santos,6/21/2025 16:15,6/21/2025 16:21\n"

// linearCSVBOMdExport is the OTHER transport at the same seam. Its first column is `ID`, the
// ROUTING KEY of source.go's write pipeline, so a BOM there does not refuse the row — it makes the
// row take the Create branch and land under a Track-derived identifier, which is #99's re-import
// duplication reintroduced for BOM'd files only.
//
// ⚠ THIS SHAPE IS NOT MEASURED IN ANY REAL LINEAR EXPORT — 0 of 45, from raw bytes, #102 — and the
// test that uses it says so in its own failure message. It is here because the FIX is at the
// shared seam and an enumeration guard is what keeps that true; it is not evidence that Linear
// emits a BOM, and nothing in this package should be read as claiming it does.
const linearCSVBOMdExport = utf8BOMLiteral +
	"ID,Team,Title,Description,Status,Priority,Created,Updated\n" +
	"IN-10,Nordic-app,License,Add licence to the nordic app,Done,High,2024-05-14T08:53:33Z,2024-05-23T16:06:19Z\n"

// TestCSVBOM_TheLanguageFactsThisRestsOn pins the three facts that make the defect possible, so a
// future Go release that changed any of them turns this red HERE rather than turning the fix into
// dead code nobody notices.
//
// ⚠ IT IS A MUST-STAY-GREEN, NOT A CATCHER. It passes before and after the fix — reverting the fix
// cannot make it fail, and it is listed as inert in the control table for exactly that reason. What
// it protects is the ARGUMENT: every sentence in this file's header depends on TrimSpace leaving
// U+FEFF alone, and an argument nothing checks is an argument that rots.
func TestCSVBOM_TheLanguageFactsThisRestsOn(t *testing.T) {
	if unicode.IsSpace('\uFEFF') {
		t.Errorf("unicode.IsSpace(U+FEFF) is now true — strings.TrimSpace would strip the BOM by " +
			"itself and this whole file describes a defect that no longer exists")
	}
	if got := strings.TrimSpace(utf8BOMLiteral + "Summary"); got != utf8BOMLiteral+"Summary" {
		t.Errorf("strings.TrimSpace(%q) = %q, want it unchanged", utf8BOMLiteral+"Summary", got)
	}
	// The key buildIndex would file the column under, computed the way buildIndex computes it.
	if key := strings.TrimSpace(strings.ToLower(utf8BOMLiteral + "Summary")); key == "summary" {
		t.Errorf("a BOM'd header cell now lowercases to %q — the lookup would hit and the defect "+
			"this file measures would be impossible", key)
	}
	// And encoding/csv's own behaviour, because if IT started stripping the byte the fix below
	// would be unreachable rather than wrong.
	rd := csv.NewReader(strings.NewReader(utf8BOMLiteral + "Summary,Issue key\nx,ENG-1\n"))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	hdr, err := rd.Read()
	if err != nil {
		t.Fatalf("csv.Read header: %v", err)
	}
	if hdr[0] != utf8BOMLiteral+"Summary" {
		t.Errorf("csv.Reader now returns header[0] = %q — it has started handling the BOM itself, "+
			"and skipUTF8BOM is dead code that should be deleted rather than kept", hdr[0])
	}
}

// TestJiraCSV_ABOMdExportImportsItsRows is THE FINDING. Before the fix this reads
// imported=0 skipped=2, and every skip carries errEmptyTitle.
func TestJiraCSV_ABOMdExportImportsItsRows(t *testing.T) {
	imp, store := newTestImporter()
	res, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(jiraCSVBOMdExport))
	if err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	if res.Imported != 2 || len(store.created) != 2 {
		t.Fatalf("imported=%d skipped=%d writes=%d, want 2 imported — a real Jira export whose only "+
			"difference from an importing one is three leading bytes (EF BB BF) landed nothing. "+
			"errors=%v\n"+
			"MEASURED: 66 of 304 real exports begin with those bytes and 63 carry them on `Summary`.",
			res.Imported, res.Skipped, len(store.created), res.Errors)
	}
	// The title is the column the BOM hides, so assert the VALUE and not just the count: a row that
	// landed with an empty title would satisfy a count assertion for the worst possible reason.
	if got := store.created[0].Title; got != "Agregar Producto" {
		t.Errorf("first issue Title = %q, want %q", got, "Agregar Producto")
	}
	// And the routing key, because a BOM'd file that imported under a DERIVED identifier would still
	// duplicate on re-import — the row landing is not the same claim as the row landing correctly.
	//
	// ⚠ THIS FAKE CANNOT SEE THE ROUTING, ONLY THE VALUE. fakeIssueStore implements Create and NOT
	// UpsertByIdentifier, so imp.upserter is nil and every row here takes the Create branch whatever
	// its Identifier says. What this asserts is that the MAPPER produced the provider key; that the
	// pipeline then routes on it is measured on real Postgres by
	// TestJobRow_JiraCSV_ABOMdExportLandsItsRowsUnderTheProviderKey.
	if got := store.created[0].Identifier; got != "QUAS-1" {
		t.Errorf("first issue Identifier = %q, want %q — the title was found but the key was not, "+
			"so this row would land under a Track-derived <team>-<n> and a re-import of the file "+
			"would write a second copy of it", got, "QUAS-1")
	}
}

// TestJiraCSV_AnExportWithNoBOMImportsExactlyAsBefore is the FLOOR. The 238 files of the measured
// corpus that carry no BOM must be untouched by this merge.
func TestJiraCSV_AnExportWithNoBOMImportsExactlyAsBefore(t *testing.T) {
	imp, store := newTestImporter()
	res, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(jiraCSVNoBOMExport))
	if err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	if res.Imported != 2 {
		t.Fatalf("imported=%d, want 2 — the ordinary shape must be unaffected. errors=%v", res.Imported, res.Errors)
	}
	if store.created[0].Title != "Agregar Producto" || store.created[0].Identifier != "QUAS-1" {
		t.Errorf("first issue = {%q,%q}, want {\"Agregar Producto\",\"QUAS-1\"}",
			store.created[0].Title, store.created[0].Identifier)
	}
}

// TestLinearCSV_ABOMdExportStillReadsTheRoutingKey is the OTHER transport at the shared seam.
func TestLinearCSV_ABOMdExportStillReadsTheRoutingKey(t *testing.T) {
	imp, store := newTestImporter()
	res, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(linearCSVBOMdExport))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if res.Imported != 1 {
		t.Fatalf("imported=%d, want 1. errors=%v", res.Imported, res.Errors)
	}
	if got := store.created[0].Identifier; got != "IN-10" {
		t.Errorf("Identifier = %q, want %q — `ID` is Linear's first column, so a BOM lands on the "+
			"ROUTING KEY: the row still imports (Title is unaffected) but takes the Create branch "+
			"and gets a derived <team>-<n>, so a re-import writes a second copy of the whole file.\n"+
			"⚠ NO REAL LINEAR EXPORT IN THE MEASURED CORPUS CARRIES A BOM (0 of 45, raw bytes, #102). "+
			"This is an enumeration guard on a shared seam, not a claim that Linear emits one.", got, "IN-10")
	}
}

// TestCSVBOM_OnlyTheFILEStartIsStripped is the OVER-CORRECTION REFUSAL, and it is the control C3
// exists to break. U+FEFF is a byte-order mark ONLY at the start of a file; anywhere else it is
// ZERO WIDTH NO-BREAK SPACE, a legitimate (if unpleasant) character that a Jira summary can
// genuinely contain. A fix that stripped it globally — from every header cell, or from cell values
// — would silently rewrite user data to make a parsing problem go away.
func TestCSVBOM_OnlyTheFILEStartIsStripped(t *testing.T) {
	// A BOM on a LATER header cell is not a byte-order mark. The column keeps its odd name, which
	// means the importer does not read it — correct, and visibly so.
	const midHeader = "Summary,Issue key," + utf8BOMLiteral + "Priority\n" +
		"Ticket one,QUAS-1,High\n"
	imp, store := newTestImporter()
	if _, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(midHeader)); err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("writes=%d, want 1", len(store.created))
	}
	if got := store.created[0].Priority; got != 0 {
		t.Errorf("Priority = %d, want 0 (PriorityNone) — a U+FEFF in the MIDDLE of a header is not "+
			"a byte-order mark, and a fix that stripped it there would be inventing a column name "+
			"the export did not use", got)
	}

	// And a BOM inside a VALUE is content. Stripping it would be editing the user's text.
	const inValue = "Summary,Issue key\n" + utf8BOMLiteral + "Odd title,QUAS-2\n"
	imp2, store2 := newTestImporter()
	if _, err := imp2.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(inValue)); err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	if len(store2.created) != 1 {
		t.Fatalf("writes=%d, want 1", len(store2.created))
	}
	if got := store2.created[0].Title; got != utf8BOMLiteral+"Odd title" {
		t.Errorf("Title = %q, want the value unchanged at %q — the importer must not rewrite cell "+
			"content to tidy up a file-level marker", got, utf8BOMLiteral+"Odd title")
	}
}

// TestCSVBOM_AFileThatIsOnlyABOMIsNotAnError guards the boundary the Peek-based implementation
// creates: a file SHORTER than three bytes, and a file that is exactly the three bytes and nothing
// else. Both must behave as an empty export (zero rows, no error), which is what newCSVSource
// already promises for empty input.
func TestCSVBOM_AFileThatIsOnlyABOMIsNotAnError(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"only the BOM", utf8BOMLiteral},
		{"shorter than a BOM", "S"},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			imp, _ := newTestImporter()
			res, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("ImportJiraCSV(%q) = error %v, want an empty result", tc.body, err)
			}
			if res.Imported != 0 || res.Skipped != 0 {
				t.Errorf("imported=%d skipped=%d, want 0/0", res.Imported, res.Skipped)
			}
		})
	}
}
