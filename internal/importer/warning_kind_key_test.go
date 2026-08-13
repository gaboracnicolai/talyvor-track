package importer

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// warning_kind_key_test.go — THE WARNING BOUND IS DEFEATED BY THE ONE FIELD IN ITS GROUP KEY THAT
// CARRIES VERBATIM PROVIDER TEXT, AND THE TEST THAT EXISTS TO ASSERT "EVERY NOTE KIND IS BOUNDED"
// HELD THAT FIELD CONSTANT.
//
// #80 bounded ImportResult.Warnings so a 10,000-row import of 10,000 distinct unknown statuses
// renders 10 exemplars + 1 summary instead of 10,000 lines. renderWarnings does that by grouping
// notes into a `kind` and applying maxWarningExemplars PER GROUP. The kind was
// (Field, Mapped, Via, ViaValue, ViaResolved) — and ViaValue is a provider cell copied verbatim:
// jiraCSVStatusCategory does `statusFallback{via: viaCategory, value: raw}` with `raw` straight off
// the uploaded CSV's "Status Category" column. A distinct cell per row is therefore a distinct KIND
// per row, each holding one note, each under the bound, each rendering its own line.
//
// MEASURED through the shipped jiraRowMapper, tallying exactly the way run() does
// (degraded[note]++ per imported row) and calling the shipped renderWarnings — rows in ⇒ lines out:
//
//	rows    A: no "Status Category" column    B: free-text "Status Category" cell
//	  10                        12                                   12
//	 100                        13                                  102
//	1000                        13                                 1002
//	5000                        13                                 5002   ← 638 KB of TEXT[]
//
// A is the control and it is the whole measurement: same rows, same per-row-distinct unknown
// statuses, one column of difference. A stays flat at 13 (two column-absence lines + 10 exemplars +
// 1 summary), B tracks the row count. The bound works exactly as #80 built it and this one field
// walks around it.
//
// ⚠⚠ THE EXISTING GUARD WATCHED THIS HAPPEN AND COULD NOT SEE IT.
// TestWarnings_EveryNoteKindIsBoundedNotJustDates carries a sub-case literally named "status
// resolved via category" — the only kind that HAS a ViaValue — and its generator pins
// `ViaValue: "indeterminate"` while varying `Value`. Holding the one per-row field constant is what
// makes the case pass: with 400 constant ViaValues there is 1 group of 400 notes, which the bound
// handles. It is repaired below rather than left as prose.
//
// ⚠ WHERE IT LANDS. The lines go into import_jobs.warnings (TEXT[], NOT NULL) in one UPDATE and out
// of GET /import/jobs/{id} in one JSON body. The upload cap is 64 MiB (jobMaxUploadBytes) and a row
// of this shape is ~28 bytes, so the reachable ceiling is ~2.4M lines / ~300 MB in one array from
// one authenticated upload. No crafted export is needed for the smaller version of it: any export
// whose "Status Category" header sits over a column of per-row text does this.
//
// ⚠ THE FIX IS THE KEY, NOT A SECOND BOUND. ViaValue is dropped from the group key and kept in the
// rendered line, so the exemplars still name the categories they always named. For a RESOLVED note
// almost nothing merges anyway — the category DECIDES Mapped, and Mapped is still in the key. For an
// UNRESOLVED one the category decided nothing, which is precisely why it must not mint a kind.

// ─── the defect, at the unit ────────────────────────────────

// bothAxesVary is the shape the transport actually produces: an unrecognised status AND an
// unplaceable category, both verbatim from the row.
func degradedCategoryPairs(distinct int) map[FieldNote]int {
	out := map[FieldNote]int{}
	for i := 0; i < distinct; i++ {
		out[FieldNote{
			Field: "status", Value: fmt.Sprintf("Statuz%d", i), Mapped: string(model.StatusBacklog),
			Via: viaCategory, ViaValue: fmt.Sprintf("Categoree%d", i),
		}]++
	}
	return out
}

func TestWarnings_AProviderValueInTheGroupKeyCannotMintUnboundedKinds(t *testing.T) {
	for _, distinct := range []int{maxWarningExemplars + 1, 500, 3000} {
		t.Run(strconv.Itoa(distinct), func(t *testing.T) {
			got := renderWarnings(degradedCategoryPairs(distinct))
			if len(got) != maxWarningExemplars+1 {
				t.Fatalf("%d distinct (status, statusCategory) pairs produced %d warning lines; "+
					"want exactly %d exemplars + 1 summary", distinct, len(got), maxWarningExemplars)
			}
			joined := strings.Join(got, "\n")
			summaries := 0
			for _, l := range got {
				if strings.HasPrefix(l, warningSummaryPrefix) {
					summaries++
				}
			}
			if summaries != 1 {
				t.Fatalf("%d summary lines, want exactly 1:\n%s", summaries, joined)
			}
			// NOTHING IS HIDDEN: the count of what was not listed must still be reported.
			if !strings.Contains(joined, fmt.Sprintf("%d further distinct", distinct-maxWarningExemplars)) {
				t.Errorf("the summary does not name the %d findings it did not list:\n%s",
					distinct-maxWarningExemplars, joined)
			}
			// AND THE EXEMPLARS STILL NAME THE CATEGORY. A "fix" that dropped ViaValue from the
			// RENDER as well would bound the report by deleting the finding, and every length
			// assertion above would still pass.
			if !strings.Contains(joined, "statusCategory") {
				t.Errorf("the exemplars no longer name the category that could not be placed:\n%s", joined)
			}
		})
	}
}

// ⚠ THE OTHER HALF OF THE SAME KEY, AND THE ONE A pairs-ONLY FIXTURE CANNOT SEE. When the STATUS
// repeats and only the CATEGORY varies, every note shares one Value — so a bound that keyed on Value
// alone would already look bounded here while the real report was one line per row. It also pins the
// summary's SENTENCE against the arithmetic: 3,000 notes over ONE status value is where "further
// distinct status VALUE(s)" would be a false count, which is why the noun names the finding.
func TestWarnings_OneStatusWithManyUnplaceableCategoriesIsBounded(t *testing.T) {
	const distinct = 3000
	degraded := map[FieldNote]int{}
	for i := 0; i < distinct; i++ {
		degraded[FieldNote{
			Field: "status", Value: "Bespoke", Mapped: string(model.StatusBacklog),
			Via: viaCategory, ViaValue: fmt.Sprintf("Categoree%d", i),
		}] = 2 // two issues each, so "findings not listed" and "issues covered" cannot be confused
	}
	got := renderWarnings(degraded)
	if len(got) != maxWarningExemplars+1 {
		t.Fatalf("%d distinct categories under ONE status produced %d lines, want %d exemplars + 1 summary",
			distinct, len(got), maxWarningExemplars)
	}
	var summary string
	for _, l := range got {
		if strings.HasPrefix(l, warningSummaryPrefix) {
			summary = l
		}
	}
	if summary == "" {
		t.Fatalf("no summary line:\n%s", strings.Join(got, "\n"))
	}
	if !strings.Contains(summary, fmt.Sprintf("%d further distinct", distinct-maxWarningExemplars)) {
		t.Errorf("summary should name %d unlisted findings: %q", distinct-maxWarningExemplars, summary)
	}
	if !strings.Contains(summary, fmt.Sprintf("across %d issue(s)", (distinct-maxWarningExemplars)*2)) {
		t.Errorf("summary should name %d issues: %q", (distinct-maxWarningExemplars)*2, summary)
	}
	// ⚠ THE SENTENCE MUST NOT CLAIM 2,990 DISTINCT STATUS VALUES — there is exactly one.
	if strings.Contains(summary, "status value(s)") {
		t.Errorf("the summary counts findings and calls them status VALUES, of which there is one: %q", summary)
	}
}

// ⚠ THE EXEMPLAR CHOICE MUST STAY DETERMINISTIC ACROSS THE AXIS THE KEY NO LONGER CARRIES.
// renderWarnings sorts so two runs of one import are diffable, and it sorted on Value alone — which
// was total while ViaValue was in the key and is NOT total now that notes sharing a Value can sit in
// one group. TestWarnings_TheSameImportRendersIdenticallyEveryTime cannot see this: its fixture
// gives every note a distinct Value, so map iteration order never reaches the comparison.
func TestWarnings_ExemplarsAreStableWhenOnlyTheProviderValueDiffers(t *testing.T) {
	build := func() map[FieldNote]int {
		degraded := map[FieldNote]int{}
		for i := 0; i < 400; i++ {
			degraded[FieldNote{
				Field: "status", Value: "Bespoke", Mapped: string(model.StatusBacklog),
				Via: viaCategory, ViaValue: fmt.Sprintf("Categoree%03d", i),
			}]++
		}
		return degraded
	}
	first := strings.Join(renderWarnings(build()), "\n")
	for i := 0; i < 12; i++ {
		if got := strings.Join(renderWarnings(build()), "\n"); got != first {
			t.Fatalf("run %d differs from run 0 — map iteration order is choosing the exemplars\n%s\n---\n%s",
				i, first, got)
		}
	}
}

// ─── the defect, through the shipped transport ──────────────

// ⚠ A HAND-BUILT FieldNote IS WHAT LET THIS THROUGH ONCE ALREADY. The existing guard's fixture
// chose its own ViaValue and chose a constant; this one does not get to choose — it hands real CSV
// bytes to the real jiraRowMapper and tallies the notes the mapper emits, the way run() does.
func tallyThroughJiraMapper(t *testing.T, header string, row func(i int) string, n int) []string {
	t.Helper()
	var b bytes.Buffer
	b.WriteString(header + "\n")
	for i := 0; i < n; i++ {
		b.WriteString(row(i) + "\n")
	}
	src, err := newCSVSource(bytes.NewReader(b.Bytes()), jiraRowMapper)
	if err != nil {
		t.Fatalf("csv source: %v", err)
	}
	degraded := map[FieldNote]int{}
	rows := 0
	for {
		r, ok := src.Next()
		if !ok {
			break
		}
		if r.Err != nil {
			t.Fatalf("row %d was refused by the mapper (%v) — this fixture must produce IMPORTED, "+
				"degraded rows, not rejected ones", r.RowNum, r.Err)
		}
		rows++
		for _, note := range r.Notes {
			degraded[note]++
		}
	}
	if rows != n {
		t.Fatalf("drained %d rows, want %d", rows, n)
	}
	return renderWarnings(degraded)
}

func TestWarnings_JiraCSVWithAFreeTextStatusCategoryIsBounded(t *testing.T) {
	const rows = 2000
	// ⚠ THE CONTROL RUNS FIRST AND IS NOT DECORATION. It is the same export minus the one column,
	// and it is what says the fixture's per-row-distinct STATUSES are already handled — so a red on
	// the case below is about the category column and nothing else.
	control := tallyThroughJiraMapper(t,
		"Issue key,Summary,Status",
		func(i int) string { return fmt.Sprintf("ENG-%d,t%d,Statuz%d", i, i, i) }, rows)
	if len(control) > maxWarningExemplars+3 { // + the Created and Updated column-absence lines
		t.Fatalf("CONTROL: %d rows with no Status Category column produced %d lines — the per-kind "+
			"bound is not working at all, so this file is measuring the wrong thing", rows, len(control))
	}
	if !strings.Contains(strings.Join(control, "\n"), "unrecognised status") {
		t.Fatalf("CONTROL: the fixture produced no status warnings at all, so a bounded case below "+
			"would prove nothing:\n%s", strings.Join(control, "\n"))
	}

	got := tallyThroughJiraMapper(t,
		"Issue key,Summary,Status,Status Category",
		func(i int) string { return fmt.Sprintf("ENG-%d,t%d,Statuz%d,Categoree%d", i, i, i, i) }, rows)
	if len(got) > len(control)+1 {
		t.Fatalf("a %d-row Jira CSV whose Status Category column carries per-row text produced %d "+
			"warning lines (the same export without that column produces %d) — one uploaded file, "+
			"one TEXT[] column, one JSON response:\n%s",
			rows, len(got), len(control), strings.Join(got[:min(6, len(got))], "\n"))
	}
}
