package importer

import (
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// csv_done_without_completion_test.go — the guards behind csv_done_without_completion.go.
//
// PROVENANCE FOR EVERY NUMBER BELOW: a loss census run 2026-08-11 over the two corpora this item
// has used since #99/#103 — /tmp/w34-linear-corpus-cache (45 real Linear exports) and
// /tmp/w34-jira-corpus (346 real Jira exports) — driving THE SHIPPED mappers row by row and
// classifying every issue that imported as `done` by whether a completion instant reached it. The
// census is reproducible from csv_done_without_completion_census_test.go in this package, which
// SKIPS where the corpus is absent (CI) and is therefore NOT a regression guard.
//
//	jira   : 7,186 done · 4,043 with completed_at · 2,288 NULL because the export has no `Resolved`
//	         column at all · 137 NULL on an empty cell · 718 NULL and already REPORTED (#111's
//	         refused ambiguous dates)
//	linear : 1,153 done · 1,119 with completed_at · 34 NULL because the export has no `Completed`
//	         column
//
// 2,425 of 7,186 Jira done issues (33.7%) and 34 of 1,153 Linear ones therefore reached
// `analytics` — whose resolution and throughput queries select on `completed_at IS NOT NULL` — as
// rows that count as neither open nor delivered, and the import that produced them reported itself
// clean. THE REFUSAL PATH WAS ALREADY LOUD (718 reported); the two ABSENCES were silent, which is
// the exact asymmetry `Created` and `Updated` do not have: both of those report viaNo*Column AND
// viaNo*Value. This file closes the gap on the third date column, and on nothing else.

// ─── the gate: an OPEN issue with no completion column must stay silent ───
//
// This is the must-stay-green half, and it is what makes the note a report rather than noise: every
// export in both corpora has open issues, so a note that fired on them would fire on every import.

func TestDoneWithoutCompletion_AnOpenIssueIsNeverReported(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		row    []string
		linear bool
	}{
		{"jira, no Resolved column, issue is open", "Summary,Status", []string{"t", "In Progress"}, false},
		{"jira, Resolved column present and empty, issue is open", "Summary,Status,Resolved", []string{"t", "To Do", ""}, false},
		{"linear, no Completed column, issue is open", "Title,Status", []string{"t", "In Progress"}, true},
		{"linear, Completed column present and empty, issue is open", "Title,Status,Completed", []string{"t", "Todo", ""}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mi := mustMap(t, tc.header, tc.row, tc.linear)
			if mi.issue.Status == model.StatusDone {
				t.Fatalf("fixture is wrong: status mapped to done, so this case cannot test the gate")
			}
			for _, n := range mi.notes {
				if n.Via == viaNoResolvedColumn || n.Via == viaNoResolvedValue ||
					n.Via == viaNoLinearCompletedColumn || n.Via == viaNoLinearCompletedValue {
					t.Errorf("an open issue produced %+v — the note must fire ONLY where the row is "+
						"invisible to `completed_at IS NOT NULL`, and an open issue is correctly invisible", n)
				}
			}
		})
	}
}

// ─── the finding: a DONE issue with no completion instant is reported ───

func TestDoneWithoutCompletion_TheTwoAbsencesAreReportedApart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		header    string
		row       []string
		linear    bool
		wantVia   string
		wantField string
		why       string
	}{
		{
			name: "jira: the export carries no Resolved column at all",
			// 2,288 real rows, from 7 of 346 exports.
			header: "Summary,Status", row: []string{"t", "Done"},
			wantVia: viaNoResolvedColumn, wantField: fieldResolutionDate,
			why: "a re-export with the column fixes this; nothing about the data does",
		},
		{
			name: "jira: the column is there and this row's cell is empty",
			// 137 real rows.
			header: "Summary,Status,Resolved", row: []string{"t", "Done", ""},
			wantVia: viaNoResolvedValue, wantField: fieldResolutionDate,
			why: "a blank cell is a data-quality note about THESE rows, not about the export shape",
		},
		{
			name: "linear: the export carries no Completed column at all",
			// 34 real rows, from 1 of 45 exports.
			header: "Title,Status", row: []string{"t", "Done"},
			linear:  true,
			wantVia: viaNoLinearCompletedColumn, wantField: fieldCompletionTime,
			why: "Linear names the field completedAt; a warning naming Jira's `Resolved` sends the operator to a column their export does not have",
		},
		{
			name:   "linear: the column is there and this row's cell is empty",
			header: "Title,Status,Completed", row: []string{"t", "Done", ""},
			linear:  true,
			wantVia: viaNoLinearCompletedValue, wantField: fieldCompletionTime,
			why: "the same two-absence split Created and Updated already make on both providers",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mi := mustMap(t, tc.header, tc.row, tc.linear)
			if mi.issue.Status != model.StatusDone {
				t.Fatalf("fixture is wrong: status mapped to %q, not done", mi.issue.Status)
			}
			if mi.issue.CompletedAt != nil {
				t.Fatalf("fixture is wrong: a completion instant reached the issue, so there is nothing to report")
			}
			var got []FieldNote
			for _, n := range mi.notes {
				if n.Via == tc.wantVia {
					got = append(got, n)
				}
			}
			if len(got) != 1 {
				t.Fatalf("notes = %+v, want exactly one with Via=%q — %s", mi.notes, tc.wantVia, tc.why)
			}
			if got[0].Field != tc.wantField {
				t.Errorf("note field = %q, want %q — the warning must speak the provider's own vocabulary",
					got[0].Field, tc.wantField)
			}
		})
	}
}

// A refused value already has its own report (#111's viaUnparseableDate, 718 real rows). Reporting
// it twice would double-count the loudest case in the census and make the new line look bigger than
// it is.
func TestDoneWithoutCompletion_ARefusedValueIsNotReportedTwice(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		row    []string
		linear bool
	}{
		{"jira", "Summary,Status,Resolved", []string{"t", "Done", "not a date"}, false},
		{"linear", "Title,Status,Completed", []string{"t", "Done", "not a date"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mi := mustMap(t, tc.header, tc.row, tc.linear)
			refused, absent := 0, 0
			for _, n := range mi.notes {
				switch n.Via {
				case viaUnparseableDate:
					refused++
				case viaNoResolvedColumn, viaNoResolvedValue, viaNoLinearCompletedColumn, viaNoLinearCompletedValue:
					absent++
				}
			}
			if refused != 1 {
				t.Errorf("the refusal note is gone (%d) — this test's whole premise is that it already exists", refused)
			}
			if absent != 0 {
				t.Errorf("a refused value ALSO produced %d absence note(s); the same loss must be reported once", absent)
			}
		})
	}
}

// A done issue that DID get its instant must stay silent, or the normal case is unreadable.
func TestDoneWithoutCompletion_AGoodValueSaysNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		row    []string
		linear bool
	}{
		{"jira", "Summary,Status,Resolved", []string{"t", "Done", "23/Jul/2026 7:36 PM"}, false},
		{"linear", "Title,Status,Completed", []string{"t", "Done", "2026-07-23T19:36:00.000Z"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mi := mustMap(t, tc.header, tc.row, tc.linear)
			if mi.issue.CompletedAt == nil {
				t.Fatalf("fixture is wrong: no instant reached the issue, so this cannot test the silent case")
			}
			for _, n := range mi.notes {
				if n.Via == viaNoResolvedColumn || n.Via == viaNoResolvedValue ||
					n.Via == viaNoLinearCompletedColumn || n.Via == viaNoLinearCompletedValue {
					t.Errorf("a successfully mapped completion produced %+v", n)
				}
			}
		})
	}
}

// The four new lines must render as four different sentences, each naming ITS provider's column.
// Keeping them apart in the struct and collapsing them in the text is the same defect one layer down.
func TestDoneWithoutCompletion_TheFourLinesRenderApart(t *testing.T) {
	lines := map[string]string{
		"jira/no-column":   FieldNote{Field: fieldResolutionDate, Via: viaNoResolvedColumn}.render(11),
		"jira/no-value":    FieldNote{Field: fieldResolutionDate, Via: viaNoResolvedValue}.render(11),
		"linear/no-column": FieldNote{Field: fieldCompletionTime, Via: viaNoLinearCompletedColumn}.render(11),
		"linear/no-value":  FieldNote{Field: fieldCompletionTime, Via: viaNoLinearCompletedValue}.render(11),
	}
	seen := map[string]string{}
	for name, line := range lines {
		if other, dup := seen[line]; dup {
			t.Errorf("%s and %s render identically: %q", name, other, line)
		}
		seen[line] = name
		if !strings.Contains(line, "11 issue(s)") {
			t.Errorf("%s does not carry its count: %q", name, line)
		}
		if !strings.Contains(line, "done") {
			t.Errorf("%s does not say the issues imported as done, which is the whole condition: %q", name, line)
		}
	}
	// Each line must name the column of ITS OWN provider and not the other's.
	if !strings.Contains(lines["jira/no-column"], `"Resolved"`) {
		t.Errorf("the Jira line does not name `Resolved`: %q", lines["jira/no-column"])
	}
	if !strings.Contains(lines["linear/no-column"], `"Completed"`) {
		t.Errorf("the Linear line does not name `Completed`: %q", lines["linear/no-column"])
	}
	if strings.Contains(lines["linear/no-column"], "Resolved") {
		t.Errorf("the Linear line names Jira's column: %q", lines["linear/no-column"])
	}
}

func mustMap(t *testing.T, header string, row []string, linear bool) mappedIssue {
	t.Helper()
	ci := buildIndex(strings.Split(header, ","))
	var mi mappedIssue
	var err error
	if linear {
		mi, err = linearRowMapper(ci, row)
	} else {
		mi, err = jiraRowMapper(ci, row)
	}
	if err != nil {
		t.Fatalf("mapper refused the row: %v", err)
	}
	return mi
}
