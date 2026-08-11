package importer

// csv_wide_row_test.go — THE ONE ROW/HEADER MISMATCH THE PARSER KNOWS ABOUT AND SAYS NOTHING ABOUT.
//
// csvSource.Next has a branch for `len(row) < s.expectedCols` and, until this merge, none for
// `len(row) > s.expectedCols`. The two are not symmetric and the silent one is the worse of the two:
//
//	NARROW  the columns past the row's end read as "" — a LOSS, visible as an empty column, and
//	        reported since #102 ("every column past the last one supplied read as empty")
//	WIDE    the surplus cell SHIFTS every column after it, so the mapper reads a NEIGHBOUR'S value.
//	        Nothing is empty. Nothing fails. The value is present, plausible and wrong.
//
// MEASURED FIRST-HAND (tab-3a71), whole population, in GO, under the EXACT reader settings
// newCSVSource uses — encoding/csv, FieldsPerRecord=-1, TrimLeadingSpace=true, no LazyQuotes, same
// UTF-8 BOM strip. Python's csv module is more tolerant than Go's and a Python census would have
// been a fact about Python:
//
//	/tmp/w34-jira-corpus     346 files · 340 parse a >1-column header with ≥1 data row · 31,103 rows
//	                         → 11 rows WIDER than their header, in 2 files, from 2 unrelated instances
//	/tmp/w34-linear-corpus-cache  46 files · 45 genuine · 3,164 rows → 0 wide rows
//
// ⚠ THE ROW COUNT IS MINE AND ITS PREDICATE IS STATED, because it disagrees with the "18,807 rows /
// 302 exports" earlier entries quote: that census counted rows-with-a-given-column over the corpus
// as it stood then, this one counts EVERY data row every file yields today. Neither is wrong; they
// are different queries and blending them would produce a number nobody measured.
//
// ⚠ 11 ROWS IS THE POPULATION AND IT IS NOT THE BLAST RADIUS. In `0347210d…` the wide rows are 10 of
// 10 — 100% of the file — so that import is wrong in every row and reports itself CLEAN. Driven
// through the shipped ImportJiraCSV over the real bytes before a line of this was written:
//
//	imported=10 skipped=0 refused=0 errors=0 warnings=7
//	  … 7 warnings about assignees, resolutions and a resolution date. NONE about the row width.
//	  ISSUE[0] title="Enhance security protocols" labels=[label1] desc="label2"
//
// The export carries TWO `Labels` cells against a header that declares ONE — Jira's ordinary
// multi-value serialisation with a header that was not widened to match. So `Description`, the next
// column the mapper reads, holds a LABEL on all ten issues. In the second file (`7f22900e…`, a
// different instance, 43 columns) the same extra cell lands one column earlier and `Labels` holds
// `REG-008` — an ISSUE KEY.
//
// ⚠ PROVENANCE, NOT DRESSED UP. These are SECOND-HAND BYTES: exports other people's instances
// produced and committed to public repositories, the same corpus and the same limit
// w34-jira-csv-corpus-probe.py states for itself. `0347210d…` reads as a generated sample (its
// people are footballers and its project is "Example Project"). What that file is evidence OF is
// the SHAPE — a Jira export whose header declares fewer multi-value columns than a row supplies —
// and the shape is corroborated by a second file from an unrelated instance at a different column.
// What it is NOT evidence of is a frequency in any one tenant's data.
//
// ⚠ THIS REPORTS, IT DOES NOT REFUSE, AND THAT IS THE PACKAGE'S OWN PRECEDENT. csvSource.Next's
// comment records that the narrow-row REFUSAL was itself the defect (#102: two whole exports
// imported nothing and were reported `failed`). No row changes where it lands here; the operator
// gains a sentence that no other output of this importer can supply.
//
// ⚠ AND THE SENTENCE DOES NOT OVERCLAIM. "Every column after it read the next column's value" is
// true only if the surplus cell did not arrive LAST — a trailing extra field misaligns nothing.
// The parser cannot tell those apart (a header is the only thing that names a column, and this row
// disagrees with it), so the rendered line states the certain half as certain and the other half as
// conditional. A note that asserted misalignment outright would be the same overclaim in the
// opposite direction.

import (
	"context"
	"strings"
	"testing"
)

// The measured shape, header VERBATIM from /tmp/w34-jira-corpus/0347210d3b5d362fbcb70d268f2cbb94
// (30 columns, `Labels` at index 23, `Description` at 24). The data rows are written here rather
// than copied so the prose stays short; their SHAPE is the file's — two Labels cells against a
// one-Labels header — and TestJiraCSV_WideRowFixturePremise asserts that before anything else runs.
const jiraCSVWideRowExport = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Project lead,Project description,Project url,Priority,Resolution,Assignee,Reporter,Creator,Created,Updated,Last Viewed,Resolved,Component/s,Due Date,Votes,Labels,Description,Environment,Original Estimate,Remaining Estimate,Time Spent,Work Ratio\n" +
	"Enhance security protocols,EXMPL-2311,2311,Epic,Resolved,EXMPL,Example Project,Software,lead,desc,https://example.com,Medium,Fixed,alice,bob,carol,11/May/24 03:26 PM,11/May/24 07:07 AM,16/May/24 03:49 AM,20/May/24 04:53 PM,Component 1,11/May/24,27,label1, label2,the real description,specific,38h,15h,37h,99%\n" +
	"Upgrade legacy systems,EXMPL-2312,2312,Story,In Progress,EXMPL,Example Project,Software,lead,desc,https://example.com,High,,alice,bob,carol,12/May/24 03:26 PM,12/May/24 07:07 AM,17/May/24 03:49 AM,,Component 1,26/May/24,3,label3, label4,another real description,specific,38h,15h,37h,88%\n"

// The same 30-column header with rows that FIT it exactly — the must-stay-green companion. Every
// assertion below that fires on the wide fixture must stay silent here, or "the guard sees a wide
// row" would be indistinguishable from "the guard fires on any Jira import".
const jiraCSVExactWidthExport = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Project lead,Project description,Project url,Priority,Resolution,Assignee,Reporter,Creator,Created,Updated,Last Viewed,Resolved,Component/s,Due Date,Votes,Labels,Description,Environment,Original Estimate,Remaining Estimate,Time Spent,Work Ratio\n" +
	"Enhance security protocols,EXMPL-2311,2311,Epic,Resolved,EXMPL,Example Project,Software,lead,desc,https://example.com,Medium,Fixed,alice,bob,carol,11/May/24 03:26 PM,11/May/24 07:07 AM,16/May/24 03:49 AM,20/May/24 04:53 PM,Component 1,11/May/24,27,label1,the real description,production,specific,38h,15h,37h\n"

// TestJiraCSV_WideRowFixturePremise asserts the fixture really is wider than its header BEFORE any
// test trusts it. The whole finding is one surplus cell; a fixture that lost it would turn every
// assertion in this file into a test of the ordinary path wearing this file's name — and it would
// pass. Same reasoning as TestJobRow_LinearCSV_ShortRowFixturePremise, for the opposite shape.
func TestJiraCSV_WideRowFixturePremise(t *testing.T) {
	lines := strings.Split(strings.TrimRight(jiraCSVWideRowExport, "\n"), "\n")
	header := strings.Count(lines[0], ",") + 1
	if header != 30 {
		t.Fatalf("header is %d columns, want the measured 30-wide shape", header)
	}
	for i, l := range lines[1:] {
		if got := strings.Count(l, ",") + 1; got != header+1 {
			t.Errorf("row %d supplies %d fields against a %d-column header, want %d (wide by exactly one)",
				i+1, got, header, header+1)
		}
	}
	// And the exact-width companion must be exactly that, or the must-stay-green assertions below
	// are vacuous in the other direction.
	lines = strings.Split(strings.TrimRight(jiraCSVExactWidthExport, "\n"), "\n")
	for i, l := range lines[1:] {
		if got := strings.Count(l, ",") + 1; got != header {
			t.Errorf("exact-width row %d supplies %d fields against a %d-column header, want %d",
				i+1, got, header, header)
		}
	}
}

// TestJiraCSV_ARowWiderThanTheHeaderIsReported is the finding. BEFORE this merge the import of
// jiraCSVWideRowExport returned imported=2, skipped=0, errors=0 and not one warning about the shape
// of the row.
func TestJiraCSV_ARowWiderThanTheHeaderIsReported(t *testing.T) {
	imp, _ := newTestImporter()
	out, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(jiraCSVWideRowExport))
	if err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	// The rows still land. This is a report, not a refusal — see the file header.
	if out.Imported != 2 || out.Skipped != 0 {
		t.Fatalf("imported=%d skipped=%d, want 2/0 — a wide row must keep importing exactly as it did",
			out.Imported, out.Skipped)
	}
	line := findWidthWarning(out.Warnings)
	if line == "" {
		t.Fatalf("no row-width warning in %q.\nMEASURED on the real export 0347210d…: 10 of 10 rows "+
			"wide, imported=10 skipped=0 errors=0, seven warnings and NONE about the width — while "+
			"every issue's Description held a label.", out.Warnings)
	}
	// BOTH NUMBERS, for the reason the narrow-row line names them: 31 of 30 dropped one trailing
	// cell, 40 of 30 means the row and the header are not the same export.
	if !strings.Contains(line, "31 of 30 columns") {
		t.Errorf("warning %q does not name both widths; the two numbers are the only thing that tells "+
			"a one-cell overrun from a file/header mismatch", line)
	}
	// The two shapes must not share a sentence: "wider" is the word that distinguishes it from the
	// narrow-row line, which is already in this report vocabulary.
	if !strings.Contains(line, "wider than the header") {
		t.Errorf("warning %q does not say the row was WIDER — the narrow-row line already says "+
			"'narrower', and an operator cannot act on the two being confused", line)
	}
	// Both halves of the consequence, and only one of them asserted as certain — see the file header.
	if !strings.Contains(line, "dropped") {
		t.Errorf("warning %q does not say the surplus cell(s) were dropped — that half is certain "+
			"and is the only data loss this note can promise", line)
	}
	if !strings.Contains(line, "unless") {
		t.Errorf("warning %q states the misalignment unconditionally; a surplus cell that arrived "+
			"LAST misaligns nothing and the parser cannot tell which happened", line)
	}
}

// TestJiraCSV_TheWideRowWarningIsCountedNotRepeated — one line with a count, never one line per row.
// ImportResult.Warnings is the bounded report #80 built precisely so a 10,000-row import of one
// unknown shape produces one sentence; a row-shaped note is the easiest place to lose that.
func TestJiraCSV_TheWideRowWarningIsCountedNotRepeated(t *testing.T) {
	imp, _ := newTestImporter()
	out, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(jiraCSVWideRowExport))
	if err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	n := 0
	for _, w := range out.Warnings {
		if strings.Contains(w, "wider than the header") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("got %d row-width warnings for 2 wide rows, want exactly 1 carrying a count: %q", n, out.Warnings)
	}
	if line := findWidthWarning(out.Warnings); !strings.Contains(line, "2 issue(s)") {
		t.Errorf("warning %q does not carry the count of affected issues", line)
	}
}

// TestJiraCSV_TheValueTheWideRowWarningIsAboutIsStillWrong pins WHAT the note is for. The mapping is
// deliberately unchanged by this merge: `Description` still reads the surplus label. If a later
// change makes this assertion fail, the note above stops being about anything and must be revisited
// rather than deleted — which is why the wrong value is pinned rather than left implicit.
func TestJiraCSV_TheValueTheWideRowWarningIsAboutIsStillWrong(t *testing.T) {
	imp, store := newTestImporter()
	if _, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(jiraCSVWideRowExport)); err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	if len(store.created) != 2 {
		t.Fatalf("created %d issues, want 2", len(store.created))
	}
	if got := store.created[0].Description; got != "label2" {
		t.Errorf("Description = %q, want the shifted %q — this is the damage the warning reports; "+
			"if the shift is gone the warning's subject is gone with it", got, "label2")
	}
	// And the exact-width row reads its own Description, so the assertion above is about the shift
	// and not about this mapper being broken generally.
	imp, store = newTestImporter()
	if _, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(jiraCSVExactWidthExport)); err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	if got := store.created[0].Description; got != "the real description" {
		t.Errorf("exact-width Description = %q, want %q", got, "the real description")
	}
}

// TestJiraCSV_AnExactWidthRowIsNotReported is the must-stay-green companion. Without it, a note that
// fired on every row would satisfy every assertion above.
func TestJiraCSV_AnExactWidthRowIsNotReported(t *testing.T) {
	imp, _ := newTestImporter()
	out, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(jiraCSVExactWidthExport))
	if err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	if out.Imported != 1 {
		t.Fatalf("imported=%d, want 1", out.Imported)
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, "wider than the header") || strings.Contains(w, "narrower than the header") {
			t.Errorf("a row of exactly the header's width produced a row-shape warning: %q", w)
		}
	}
}

// TestCSV_TheTwoRowShapeWarningsStayApart — a narrow row must still get the narrow sentence and must
// NOT get the wide one. The two notes share a Field ("row width"); only Via keeps them apart, and a
// single mis-set constant would collapse them into one line that is false half the time.
func TestCSV_TheTwoRowShapeWarningsStayApart(t *testing.T) {
	const narrow = "Summary,Status,Priority,Description\n" +
		"Only a title,To Do,High\n"
	imp, _ := newTestImporter()
	out, err := imp.ImportJiraCSV(context.Background(), "ws-1", "team-1", strings.NewReader(narrow))
	if err != nil {
		t.Fatalf("ImportJiraCSV: %v", err)
	}
	got := strings.Join(out.Warnings, "\n")
	if !strings.Contains(got, "narrower than the header") {
		t.Errorf("the narrow-row report is gone: %q", out.Warnings)
	}
	if strings.Contains(got, "wider than the header") {
		t.Errorf("a narrow row was reported as wide: %q", out.Warnings)
	}
}

// TestLinearCSV_ARowWiderThanTheHeaderIsReported — the seam is SHARED, so the report must be too.
// ⚠ THE LINEAR CORPUS HAS ZERO WIDE ROWS (0 of 3,164 across 46 files, measured) and this test is
// deliberately kept anyway: csvSource is the one place both transports pass through, and a fix that
// silently only covered the provider whose corpus happened to carry the shape would be exactly the
// "the supported client is blind to its own hole" trap. This asserts the seam, not a population.
func TestLinearCSV_ARowWiderThanTheHeaderIsReported(t *testing.T) {
	const wide = "ID,Title,Description,Status,Priority\n" +
		"LIN-1,Bug in cache layer,Cache invalidation is broken,Backlog,Urgent,surplus\n"
	imp, _ := newTestImporter()
	out, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(wide))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if out.Imported != 1 {
		t.Fatalf("imported=%d, want 1 — a wide row still lands", out.Imported)
	}
	line := findWidthWarning(out.Warnings)
	if !strings.Contains(line, "6 of 5 columns") {
		t.Errorf("Linear wide row not reported (warnings %q) — the seam is shared with Jira and the "+
			"report must not be provider-specific", out.Warnings)
	}
}

func findWidthWarning(ws []string) string {
	for _, w := range ws {
		if strings.Contains(w, "wider than the header") {
			return w
		}
	}
	return ""
}
