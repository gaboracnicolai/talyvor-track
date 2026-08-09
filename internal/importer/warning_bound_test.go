package importer

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// warning_bound_test.go — ImportResult.Warnings collapses by DISTINCT VALUE, so a note kind whose
// value varies per row produces ONE LINE PER ROW, into a single TEXT[] column and a single JSON
// response.
//
// ⚠ THE INVARIANT IS WRITTEN DOWN IN THIS PACKAGE AND IS NOT HELD. FieldNote's own doc comment says:
// "The pipeline COUNTS these (map[FieldNote]int) rather than accumulating a string per row: a
// 10,000-row import of one unknown status must produce one warning, not ten thousand." That is
// exactly right for ONE unknown status. The key includes the VALUE, so it says nothing at all about
// ten thousand DIFFERENT ones.
//
// MEASURED on real Postgres through the async runner, before this merge (rows in ⇒ warning lines out):
//
//	3,000 distinct unparseable Due Dates   ⇒ 3,000 lines
//	3,000 distinct unknown Statuses        ⇒ 3,000 lines
//	3,000 distinct unknown Priorities      ⇒ 3,000 lines
//	3,000 rows of ONE repeated status      ⇒         1 line   ← the control: the design works when
//	                                                             values repeat, and only then
//	20,000 distinct unparseable Due Dates  ⇒ 20,000 lines
//
// ⚠ #78 RECORDED THIS AS A DATE PROBLEM — "a date is per-row unique by NATURE" — AND THE MEASUREMENT
// SAYS THE FIELD IS NOT THE POINT. Every note kind carries it. A Jira CSV whose Status column holds
// free text, or whose status column was mis-identified so the mapper reads a summary, produces one
// line per row from a code path nobody associates with dates. The bound therefore covers EVERY note
// kind rather than special-casing viaUnparseableDate.
//
// ⚠ WHAT THE BOUND DOES NOT DO, said plainly: it changes how much is ENUMERATED, never what is
// COUNTED. The number of affected issues and the number of distinct values are both preserved and
// both reported, so no import is quieter about how much it could not place than it is today.

// A group of degraded rows with `distinct` different values, one issue each.
func degradedDates(distinct int) map[FieldNote]int {
	out := map[FieldNote]int{}
	for i := 0; i < distinct; i++ {
		out[FieldNote{Field: fieldDueDate, Value: fmt.Sprintf("2025-01-01T00:00:00.%09dZ", i), Via: viaUnparseableDate}]++
	}
	return out
}

// ─── the defect ─────────────────────────────────────────────

func TestWarnings_AreBoundedRegardlessOfHowManyDistinctValuesArrive(t *testing.T) {
	for _, distinct := range []int{maxWarningExemplars + 1, 500, 3000} {
		t.Run(strconv.Itoa(distinct), func(t *testing.T) {
			got := renderWarnings(degradedDates(distinct))

			if len(got) != maxWarningExemplars+1 {
				t.Fatalf("%d distinct values produced %d warning lines; want exactly %d exemplars + 1 summary",
					distinct, len(got), maxWarningExemplars)
			}
			// ⚠ A LENGTH CHECK ALONE IS VACUOUS AT distinct == bound+1, where the unbounded code
			// already produces bound+1 lines. The summary must actually BE there.
			joined := strings.Join(got, "\n")
			summaries := 0
			for _, l := range got {
				if strings.HasPrefix(l, warningSummaryPrefix) {
					summaries++
				}
			}
			if summaries != 1 {
				t.Fatalf("%d summary lines at %d distinct values, want exactly 1:\n%s", summaries, distinct, joined)
			}
			// AND NOTHING IS HIDDEN: the true totals must still be reported.
			if !strings.Contains(joined, strconv.Itoa(distinct-maxWarningExemplars)) {
				t.Errorf("the summary does not name the %d values it did not list:\n%s",
					distinct-maxWarningExemplars, joined)
			}
		})
	}
}

// ⚠ THE TWO NUMBERS IN THE SUMMARY MUST BE DISTINGUISHABLE, WHICH THE FIXTURES ABOVE CANNOT DO.
// degradedDates gives every value a count of 1, so "distinct values not listed" and "issues they
// covered" are the SAME NUMBER and a summary that printed one where the other belongs would be
// invisible — mutating the code proves the test reacts, not that the inputs can tell the mutations
// apart. Here each distinct value covers THREE issues, so the two numbers differ by construction.
func TestWarnings_TheSummaryDistinguishesValuesFromIssues(t *testing.T) {
	const distinct, perValue = 60, 3
	degraded := map[FieldNote]int{}
	for i := 0; i < distinct; i++ {
		degraded[FieldNote{Field: fieldDueDate, Value: fmt.Sprintf("shape-%03d", i), Via: viaUnparseableDate}] = perValue
	}
	got := renderWarnings(degraded)
	var summary string
	for _, l := range got {
		if strings.HasPrefix(l, warningSummaryPrefix) {
			summary = l
		}
	}
	if summary == "" {
		t.Fatalf("no summary line at %d distinct values:\n%s", distinct, strings.Join(got, "\n"))
	}
	wantValues := distinct - maxWarningExemplars              // 50 distinct values not listed
	wantIssues := (distinct - maxWarningExemplars) * perValue // 150 issues they covered
	if !strings.Contains(summary, fmt.Sprintf("%d further distinct", wantValues)) {
		t.Errorf("summary should name %d further distinct values: %q", wantValues, summary)
	}
	if !strings.Contains(summary, fmt.Sprintf("across %d issue(s)", wantIssues)) {
		t.Errorf("summary should name %d issues: %q", wantIssues, summary)
	}
}

// The bound is per NOTE KIND, so a noisy date column cannot crowd a status finding out of the report
// entirely — two different problems are two different findings.
func TestWarnings_ANoisyKindDoesNotCrowdOutAnother(t *testing.T) {
	degraded := degradedDates(2000)
	degraded[FieldNote{Field: "status", Value: "Deployed", Mapped: string(model.StatusBacklog)}] = 7

	got := renderWarnings(degraded)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, `"Deployed"`) {
		t.Fatalf("the status finding was crowded out by 2000 date values:\n%s", joined)
	}
	if !strings.Contains(joined, "on 7 issue(s)") {
		t.Errorf("the status finding lost its count:\n%s", joined)
	}
}

// Every kind is bounded, not just dates — the half of the measurement #78's note did not predict.
func TestWarnings_EveryNoteKindIsBoundedNotJustDates(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(i int) FieldNote
	}{
		{"status", func(i int) FieldNote {
			return FieldNote{Field: "status", Value: fmt.Sprintf("Bespoke State %d", i), Mapped: string(model.StatusBacklog)}
		}},
		{"priority", func(i int) FieldNote {
			return FieldNote{Field: "priority", Value: fmt.Sprintf("Sev-%d", i), Mapped: "0"}
		}},
		{"resolution date refused as not-done", func(i int) FieldNote {
			return FieldNote{Field: fieldResolutionDate, Value: fmt.Sprintf("25/Mar/2025 10:%02d AM", i%60),
				Mapped: string(model.StatusTodo), Via: viaStatusNotDone}
		}},
		{"status resolved via category", func(i int) FieldNote {
			return FieldNote{Field: "status", Value: fmt.Sprintf("Custom %d", i), Mapped: string(model.StatusInProgress),
				Via: viaCategory, ViaValue: "indeterminate", ViaResolved: true}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			degraded := map[FieldNote]int{}
			for i := 0; i < 400; i++ {
				degraded[tc.make(i)]++
			}
			// Some generators collide by design (the %60 one); bound against what actually arrived.
			if got := renderWarnings(degraded); len(got) > maxWarningExemplars+1 {
				t.Fatalf("%d distinct %s values produced %d lines, want at most %d",
					len(degraded), tc.name, len(got), maxWarningExemplars+1)
			}
		})
	}
}

// ─── the floors: what must NOT change ───────────────────────

// AT OR UNDER THE BOUND THE REPORT IS BYTE-IDENTICAL TO TODAY'S. This is what keeps the merge from
// re-litigating the wording #74, #77 and #78 each pinned by test — those fixtures carry one or two
// distinct values, and they must stay green without being touched.
func TestWarnings_AtOrUnderTheBoundNothingChanges(t *testing.T) {
	for _, distinct := range []int{1, 2, maxWarningExemplars} {
		t.Run(strconv.Itoa(distinct), func(t *testing.T) {
			got := renderWarnings(degradedDates(distinct))
			if len(got) != distinct {
				t.Fatalf("%d distinct values produced %d lines, want exactly %d — no summary may appear "+
					"at or under the bound", distinct, len(got), distinct)
			}
			for _, line := range got {
				if strings.HasPrefix(line, warningSummaryPrefix) {
					t.Errorf("a summary line appeared at %d distinct values:\n%s", distinct, line)
				}
				// The existing sentence, unchanged.
				if !strings.Contains(line, "is not a date shape this importer recognises — not recorded") {
					t.Errorf("the pinned wording changed: %q", line)
				}
			}
		})
	}
}

// #72's design still works: one repeated value is ONE line carrying the row count, however many rows.
func TestWarnings_OneRepeatedValueIsStillOneLineWithItsCount(t *testing.T) {
	got := renderWarnings(map[FieldNote]int{
		{Field: "status", Value: "Deployed", Mapped: string(model.StatusBacklog)}: 10000,
	})
	if len(got) != 1 {
		t.Fatalf("warnings = %d lines, want 1:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], "on 10000 issue(s)") {
		t.Errorf("the count was lost: %q", got[0])
	}
}

// Sorted output is what makes two runs of the same import diffable — the reason renderWarnings sorts
// at all. A bound that picked its exemplars nondeterministically would destroy that quietly.
func TestWarnings_TheSameImportRendersIdenticallyEveryTime(t *testing.T) {
	first := strings.Join(renderWarnings(degradedDates(900)), "\n")
	for i := 0; i < 12; i++ {
		if got := strings.Join(renderWarnings(degradedDates(900)), "\n"); got != first {
			t.Fatalf("run %d differs from run 0 — map iteration order is reaching the report\n%s\n---\n%s",
				i, first, got)
		}
	}
	if !sortedAscending(strings.Split(first, "\n")) {
		t.Errorf("the lines are not sorted:\n%s", first)
	}
}

func sortedAscending(lines []string) bool {
	for i := 1; i < len(lines); i++ {
		if lines[i-1] > lines[i] {
			return false
		}
	}
	return true
}

// The empty case stays an empty, NON-NIL slice: run() seeds Warnings to []string{} and Finish writes
// '{}' rather than NULL into a NOT NULL column.
func TestWarnings_NoDegradedRowsIsEmptyNotNil(t *testing.T) {
	got := renderWarnings(map[FieldNote]int{})
	if got == nil {
		t.Fatalf("renderWarnings returned nil; Finish would write NULL into a NOT NULL TEXT[]")
	}
	if len(got) != 0 {
		t.Errorf("warnings = %v, want empty", got)
	}
}
