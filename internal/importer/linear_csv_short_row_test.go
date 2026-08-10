package importer

// linear_csv_short_row_test.go — A REAL LINEAR EXPORT WHOSE ROWS OMIT THE FINAL EMPTY FIELD WAS
// REFUSED ROW BY ROW, AND THE WHOLE FILE IMPORTED NOTHING.
//
// ⚠ THE FINDING IS AN ASYMMETRY INSIDE ONE PACKAGE, and both halves are three lines apart.
// columnIndex.get's own doc comment says it "Returns "" if the column doesn't exist OR THE ROW IS
// TOO SHORT — that lets row-level validation focus on what's required (title) rather than how the
// export was shaped", and getAll skips any index past the end of the row. Two accessors written to
// tolerate a short row, documented as such. csvSource.Next refused the row before either could run:
//
//	if len(row) < s.expectedCols { return ...Err: "row N: expected X columns, got Y" }, true
//
// So the bounds guard in BOTH accessors was structurally unreachable — every index comes from the
// header, so it is < expectedCols, and no row narrower than expectedCols ever reached a mapper.
//
// ⚠ THE MEASUREMENT, whole-population, over the same corpus of REAL Linear CSV exports #99/#100/#101
// used (scripts/w34-linear-csv-short-row-probe.py re-runs it, negative controls first):
//
//	45 files · 3,099 data rows · 73 rows (2.4%) NARROWER THAN THEIR HEADER, from TWO UNRELATED OWNERS
//	every one of the 73 short by EXACTLY ONE column, and in all 73 the missing header is `Roadmaps`,
//	the LAST column of the 30-wide shape and empty in every file that does emit it
//
//	pathliving/nordic/backlog/Linear-tasks-backlog-backup.csv   28 rows, 28 short  (100%)
//	wubin28/…/linear-issue-template.csv                          45 rows, 45 short  (100%)
//
// ⚠⚠ IT IS NOT 2.4% OF THE DAMAGE, IT IS TWO FILES OUT OF FORTY-FIVE THAT IMPORT NOTHING AT ALL.
// The short rows are 100% of the data rows in both files they appear in, so those two exports
// produced ZERO issues and reported every row as a failure.
//
// ⚠ ALIGNMENT WAS MEASURED, NOT ASSUMED — this is the whole reason refusing them is wrong rather
// than merely strict. A short row COULD be a misaligned one, and importing a misaligned row would
// write garbage. For all 73, at their header index:
//
//	ID matches ^PREFIX-<int>$        73/73
//	Title non-empty                  73/73
//	Status in mapLinearStatus        73/73
//	Priority in mapLinearPriority    73/73
//	Created DATE-SHAPED at its index 73/73   ⚠ and PARSING under a pinned layout: 0/73 —
//	                                         all 73 carry JavaScript's Date.toString, the shape
//	                                         #101 measured at 25.3% and deliberately refused. The
//	                                         alignment question and the parsing question have
//	                                         different answers here, and only the first one is
//	                                         about whether refusing the ROW was right.
//	                                         TestLinearCSV_AShortRowStillRefusesAnUnpinnedDateShape
//	                                         pins the second, on those verbatim bytes.
//
// Every column this importer reads is present, in the right place, and well-formed. What was
// missing was a trailing field nothing reads.
//
// ⚠ THE SHORT ROWS WERE INVISIBLE TO EVERY PROBE ON THIS ITEM, INCLUDING THE ONE THAT FOUND THEM.
// #99, #100 and #101 all count rows behind `if len(row) != len(hdr): continue`, so "3,026 rows"
// appears in three merges and the real figure is 3,099. The 73 the product refuses are exactly the
// 73 no measurement of it had ever looked at. (#89's shape: the instrument was the fixture.)

import (
	"context"
	"strings"
	"testing"
	"time"
)

// linearCSVShortRowExport is the measured shape, byte-for-byte in structure: the 30-column header
// of the two affected files, and data rows carrying 29 fields — every field present except the
// trailing `Roadmaps`. The values are real ones out of the measured population (IN-10 "License"
// from pathliving/nordic, whose 28 of 28 rows are in this state).
//
// ⚠ THE TRAILING COMMA IS THE FIXTURE. A row ending `,,` supplies an empty final field and is
// exactly 30 wide; a row ending `,` is 29 wide. That one byte is the whole difference between the
// files that import and the files that do not, which is why this constant is written out in full
// rather than generated — a generator is one loop away from silently emitting the wide shape and
// making every assertion below vacuous.
const linearCSVShortRowExport = "ID,Team,Title,Description,Status,Estimate,Priority,Project ID,Project,Creator,Assignee,Labels,Cycle Number,Cycle Name,Cycle Start,Cycle End,Created,Updated,Started,Triaged,Completed,Canceled,Archived,Due Date,Parent issue,Initiatives,Project Milestone ID,Project Milestone,SLA Status,Roadmaps\n" +
	"IN-10,Nordic-app,License,Add licence to the nordic app,Done,,High,,,pathliving@gmail.com,pathliving@gmail.com,Improvement,,,,,2024-05-14T08:53:33Z,2024-05-23T16:06:19Z,,,2024-05-23T16:06:19Z,,,,,,,,\n" +
	"IN-11,Nordic-app,Design tokens,Body two,Todo,,Medium,,,pathliving@gmail.com,pathliving@gmail.com,Improvement,,,,,2024-05-14T09:01:00Z,2024-05-23T16:10:00Z,,,,,,,,,,,\n"

// linearCSVWideRowExport is the SAME two issues with the final field supplied — the shape 43 of the
// 45 measured files use. It is the must-stay-green companion: whatever this merge does to the short
// shape, the wide shape has to keep importing exactly as it always has.
const linearCSVWideRowExport = "ID,Team,Title,Description,Status,Estimate,Priority,Project ID,Project,Creator,Assignee,Labels,Cycle Number,Cycle Name,Cycle Start,Cycle End,Created,Updated,Started,Triaged,Completed,Canceled,Archived,Due Date,Parent issue,Initiatives,Project Milestone ID,Project Milestone,SLA Status,Roadmaps\n" +
	"IN-10,Nordic-app,License,Add licence to the nordic app,Done,,High,,,pathliving@gmail.com,pathliving@gmail.com,Improvement,,,,,2024-05-14T08:53:33Z,2024-05-23T16:06:19Z,,,2024-05-23T16:06:19Z,,,,,,,,,\n" +
	"IN-11,Nordic-app,Design tokens,Body two,Todo,,Medium,,,pathliving@gmail.com,pathliving@gmail.com,Improvement,,,,,2024-05-14T09:01:00Z,2024-05-23T16:10:00Z,,,,,,,,,,,,\n"

// linearCSVShortRowDateToStringExport is the SAME short shape carrying pathliving/nordic's dates
// VERBATIM — JavaScript's Date.toString, the shape #101 measured at 746 of 2,947 real `Updated`
// cells (25.3%, six unrelated owners) and DELIBERATELY did not add to linearCSVTimeLayouts.
//
// ⚠⚠ IT IS HERE BECAUSE MY FIRST FIXTURE USED THESE BYTES AND TWO DATE ASSERTIONS WENT RED AFTER
// THE FIX WAS ALREADY WORKING. The rows imported; the dates did not parse — not because of this
// merge, but because refusing an unpinned date shape is somebody else's measured, deliberate
// decision, and my fixture had quietly bundled the two questions into one assertion. The dates in
// the fixtures above were moved to RFC3339 (a pinned layout, and the shape eight of the corpus's
// owners emit) so that those tests measure the ROW WIDTH and nothing else, and this constant keeps
// the real bytes where they can pin the other half.
const linearCSVShortRowDateToStringExport = "ID,Team,Title,Description,Status,Estimate,Priority,Project ID,Project,Creator,Assignee,Labels,Cycle Number,Cycle Name,Cycle Start,Cycle End,Created,Updated,Started,Triaged,Completed,Canceled,Archived,Due Date,Parent issue,Initiatives,Project Milestone ID,Project Milestone,SLA Status,Roadmaps\n" +
	"IN-10,Nordic-app,License,Add licence to the nordic app,Done,,High,,,pathliving@gmail.com,pathliving@gmail.com,Improvement,,,,,Tue May 14 2024 08:53:33 GMT+0000 (GMT),Thu May 23 2024 16:06:19 GMT+0000 (GMT),,,Thu May 23 2024 16:06:19 GMT+0000 (GMT),,,,,,,,\n"

// TestLinearCSV_AShortRowsRealDatesLandAndTheNarrowRowIsStillReported composes the two questions
// this row carries — its WIDTH and its DATE SHAPE — on the one fixture in the package made of real
// corpus bytes rather than of a formatted constant.
//
// ⚠⚠ THE DATE HALF INVERTED AND THE BYTES DID NOT MOVE. This test used to assert that
// `Tue May 14 2024 08:53:33 GMT+0000 (GMT)` is REFUSED, because #101 measured the shape at 25.3% of
// the corpus and left linearCSVTimeLayouts alone on the stated ground that its provenance was
// undecidable. It is decidable from the HEADER: this fixture's own 30-column header — ending
// `Project Milestone ID, Project Milestone, SLA Status, Roadmaps` — is Linear's published export
// shape, and the corpus's 34-column toString file carries a header byte-identical to three
// unrelated owners' ISO files. One exporter, two renderings. So the row's dates are now read.
//
// ⚠ THE FIXTURE IS DELIBERATELY NOT RE-DATED. Rewriting these cells to RFC3339 would keep the test
// green while removing the only real toString bytes in the package, which is how the column went
// unmeasured for thirty-one merges in the first place: a fixture that cannot carry the value cannot
// fail when the value is dropped.
//
// ⚠ THE REFUSAL PROPERTY IS STILL PINNED, one file over and one package level up — the
// `15/01/2026 10:23` cases in TestParseLinearCSVTime_TheStripIsAnchoredToTheOffset and
// TestJobRow_LinearCSV_AnUnknownDateShapeIsStillRefusedAndReported. Widening one measured shape is
// not becoming a tolerant parser, and that is asserted rather than asserted-about.
func TestLinearCSV_AShortRowsRealDatesLandAndTheNarrowRowIsStillReported(t *testing.T) {
	imp, store := newTestImporter()
	res, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(linearCSVShortRowDateToStringExport))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if res.Imported != 1 || len(store.created) != 1 {
		t.Fatalf("imported=%d creates=%d, want 1/1 — the ROW must land regardless of its date shape",
			res.Imported, len(store.created))
	}
	// The three instants these real bytes denote. Written out by hand from the fixture's own
	// cells rather than parsed with the layout under test.
	for _, want := range []struct {
		field string
		got   time.Time
		when  time.Time
	}{
		{"CreatedAt", store.created[0].CreatedAt, time.Date(2024, 5, 14, 8, 53, 33, 0, time.UTC)},
		{"UpdatedAt", store.created[0].UpdatedAt, time.Date(2024, 5, 23, 16, 6, 19, 0, time.UTC)},
		{"CompletedAt", derefTime(store.created[0].CompletedAt), time.Date(2024, 5, 23, 16, 6, 19, 0, time.UTC)},
	} {
		if !want.got.Equal(want.when) {
			t.Errorf("%s = %v, want %s — these are a REAL export's bytes, and a defaulted column "+
				"here is the import instant rather than a null anybody can spot",
				want.field, want.got, want.when.Format(time.RFC3339))
		}
	}
	joined := strings.Join(res.Warnings, "\n")
	if strings.Contains(joined, "not a date shape this importer recognises") {
		t.Errorf("warnings = %v — no cell in this row is unpinned any more; a warning naming one "+
			"means the parse regressed and the row is silently carrying import-instant timestamps",
			res.Warnings)
	}
	if !strings.Contains(joined, "narrower than the header") {
		t.Errorf("warnings = %v, want the narrow-row line — the WIDTH half of this row is what the "+
			"fixture exists for and it is unaffected by the date question", res.Warnings)
	}
}

// derefTime reads an optional instant as a value so the table above can hold all three columns.
// A nil pointer reads as the zero time, which fails the comparison loudly rather than panicking.
func derefTime(p *time.Time) time.Time {
	if p == nil {
		return time.Time{}
	}
	return *p
}

// TestLinearCSV_ShortRowFixtureIsActuallyShort is the FIXTURE PREMISE, asserted before anything
// reads it. Every assertion in this file is about a row narrower than its header; a fixture that
// quietly had the right width would make all of them pass for the wrong reason, and a trailing
// comma is not something a reader can count by eye. This test fails if the constants stop being
// what their names say.
func TestLinearCSV_ShortRowFixtureIsActuallyShort(t *testing.T) {
	check := func(name, body string, wantDelta int) {
		t.Helper()
		lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		if len(lines) < 2 {
			t.Fatalf("%s: fixture has no data rows", name)
		}
		header := strings.Count(lines[0], ",") + 1
		for i, l := range lines[1:] {
			got := strings.Count(l, ",") + 1
			if got != header+wantDelta {
				t.Errorf("%s row %d: %d fields against a %d-column header (delta %+d), want delta %+d",
					name, i+1, got, header, got-header, wantDelta)
			}
		}
		if header != 30 {
			t.Errorf("%s: header is %d columns, want the measured 30-wide shape", name, header)
		}
	}
	check("linearCSVShortRowExport", linearCSVShortRowExport, -1)
	check("linearCSVWideRowExport", linearCSVWideRowExport, 0)
}

// TestLinearCSV_ARowMissingOnlyItsTrailingFieldStillImports is the finding at the source level.
//
// BEFORE: Imported=0, Skipped=2, Errors=["row 2: expected 30 columns, got 29", "row 3: …"].
func TestLinearCSV_ARowMissingOnlyItsTrailingFieldStillImports(t *testing.T) {
	imp, store := newTestImporter()
	res, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(linearCSVShortRowExport))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if res.Imported != 2 || res.Skipped != 0 {
		t.Errorf("imported=%d skipped=%d errors=%v, want imported=2 skipped=0.\n"+
			"MEASURED: 73 of 3,099 rows in 45 real Linear exports are narrower than their header, "+
			"every one by exactly the trailing `Roadmaps` field, and in the two files that carry them "+
			"they are 100%% of the data. Refusing the row throws away every column this importer DOES "+
			"read — all of which were measured present and well-formed on all 73.",
			res.Imported, res.Skipped, res.Errors)
	}
	if len(store.created) != 2 {
		t.Fatalf("store saw %d creates, want 2", len(store.created))
	}
}

// TestLinearCSV_TheShortRowKeepsEveryColumnTheImporterReads is the half that says refusing was
// throwing away DATA rather than a formatting nicety. It asserts the mapped fields one by one,
// because "imported=2" is satisfiable by two rows of nothing.
func TestLinearCSV_TheShortRowKeepsEveryColumnTheImporterReads(t *testing.T) {
	imp, store := newTestImporter()
	if _, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(linearCSVShortRowExport)); err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if len(store.created) == 0 {
		t.Fatal("premise: nothing imported, so there is no mapping to assert on")
	}
	got := store.created[0]
	if got.Identifier != "IN-10" {
		t.Errorf("Identifier = %q, want %q — the ROUTING KEY of source.go's write pipeline; without it "+
			"a re-import writes a second copy of the row", got.Identifier, "IN-10")
	}
	if got.Title != "License" {
		t.Errorf("Title = %q, want %q", got.Title, "License")
	}
	if string(got.Status) != "done" {
		t.Errorf("Status = %q, want %q", got.Status, "done")
	}
	if int(got.Priority) != 2 {
		t.Errorf("Priority = %d, want 2 (High)", got.Priority)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero — the `Created` column was present at its header index on all 73 " +
			"measured short rows, so an import that drops it records the issue as opened today")
	}
	if got.CompletedAt == nil {
		t.Error("CompletedAt is nil — `Completed` was present on this row")
	}
	if len(got.Labels) != 1 || got.Labels[0] != "Improvement" {
		t.Errorf("Labels = %v, want [Improvement]", got.Labels)
	}
}

// TestLinearCSV_AShortRowIsReportedRatherThanSilent is the other half of the fix, and it is the one
// that keeps this from being a loosening. A row that arrived narrower than its header MUST still say
// so: the columns past the end read as empty, and on a differently-truncated export that could be a
// column the importer does read. Before this merge the operator was told (loudly, per row, in
// Errors); after it they are told once, with a count, in Warnings. What must never happen is
// neither.
func TestLinearCSV_AShortRowIsReportedRatherThanSilent(t *testing.T) {
	imp, _ := newTestImporter()
	res, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(linearCSVShortRowExport))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	var found string
	for _, w := range res.Warnings {
		if strings.Contains(w, "narrower than the header") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("no warning naming a narrow row; warnings=%v.\n"+
			"Importing the row is only half the fix — an import that quietly reads a truncated column "+
			"as empty is the structural-zero class this package reports everywhere else.", res.Warnings)
	}
	// ONE line for BOTH rows, not one per row. #80's bound is the reason: a 10,000-row export in
	// this shape must not produce 10,000 lines. The count carries the size.
	if !strings.Contains(found, "2 issue(s)") {
		t.Errorf("warning = %q, want it to carry the count of affected issues (2)", found)
	}
	if strings.Count(strings.Join(res.Warnings, "\n"), "narrower than the header") != 1 {
		t.Errorf("warnings=%v, want exactly ONE narrow-row line for two rows", res.Warnings)
	}
}

// TestLinearCSV_TheWideShapeIsUnchanged is the must-stay-green companion. 43 of the 45 measured
// files are this shape; a merge that fixed the short one and moved the wide one would be a
// regression on 96% of the population.
//
// ⚠ IT ALSO ASSERTS THE ABSENCE OF THE NEW WARNING. Without that, a change that emitted the
// narrow-row line for EVERY row would satisfy every other test in this file.
func TestLinearCSV_TheWideShapeIsUnchanged(t *testing.T) {
	imp, store := newTestImporter()
	res, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(linearCSVWideRowExport))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if res.Imported != 2 || res.Skipped != 0 || len(res.Errors) != 0 {
		t.Errorf("wide shape: imported=%d skipped=%d errors=%v, want 2/0/none", res.Imported, res.Skipped, res.Errors)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "narrower than the header") {
			t.Errorf("wide shape produced a narrow-row warning: %q", w)
		}
	}
	if len(store.created) != 2 {
		t.Errorf("store saw %d creates, want 2", len(store.created))
	}
}

// TestLinearCSV_ARowTruncatedPastTheTitleIsStillRefused is the floor, and it is the assertion that
// makes the change a NARROWING of the refusal rather than its removal. The refusal existed to stop a
// mangled row being written; a row cut back past `Title` is exactly that, and it must still not
// land. It is refused by the mapper's own errEmptyTitle — the validation columnIndex.get's doc
// comment says is supposed to be doing this job.
//
// ⚠ THIS IS THE CASE THAT SEPARATES "narrow the guard" FROM "delete the guard", and it is the one a
// reviewer should look at first.
func TestLinearCSV_ARowTruncatedPastTheTitleIsStillRefused(t *testing.T) {
	// Three fields of a thirty-column header: ID, Team, and nothing else. `Title` is column 2.
	const truncated = "ID,Team,Title,Description,Status,Estimate,Priority,Project ID,Project,Creator,Assignee,Labels,Cycle Number,Cycle Name,Cycle Start,Cycle End,Created,Updated,Started,Triaged,Completed,Canceled,Archived,Due Date,Parent issue,Initiatives,Project Milestone ID,Project Milestone,SLA Status,Roadmaps\n" +
		"IN-12,Nordic-app\n"
	imp, store := newTestImporter()
	res, err := imp.ImportLinearCSV(context.Background(), "ws-1", "team-1", strings.NewReader(truncated))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if res.Imported != 0 || res.Skipped != 1 {
		t.Errorf("imported=%d skipped=%d, want 0/1 — a row truncated past its title must not land",
			res.Imported, res.Skipped)
	}
	if len(store.created) != 0 {
		t.Errorf("store saw %d creates, want 0", len(store.created))
	}
	// ⚠ THE COUNT ALONE IS NOT THE FLOOR — IT PASSES BEFORE THIS MERGE TOO, for the wrong reason.
	// Before, the width check refused this row ("expected 30 columns, got 2"); after, the mapper's
	// errEmptyTitle does. Both give skipped=1, so a count-only assertion here would have been
	// green either way and could not tell a working floor from a deleted one. The MESSAGE is what
	// distinguishes them, so the message is what is asserted.
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "no title") {
		t.Errorf("errors = %v, want one naming the missing TITLE.\n"+
			"If this reads \"expected N columns\" the width refusal is still in front of the mapper "+
			"and the merge did not land; if it is empty the row was written.", res.Errors)
	}
}
