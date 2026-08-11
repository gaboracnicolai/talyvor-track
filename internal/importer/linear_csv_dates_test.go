package importer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/model"
)

// linear_csv_dates_test.go — the two date columns the Linear CSV transport has never read.
//
// ⚠ WHY THIS TRANSPORT AND WHY NOW. W3.4 has landed EIGHT merges on the date columns of the other
// three transports (#74 jira_api completed · #78 jira_csv due/resolved · #83 jira_csv created ·
// #84 both APIs created · #85/#86 updated) and left `linear_csv` untouched throughout, on a stop
// reason recorded in the queue as "LINEAR CSV, blocked on an unmeasurable export". THAT REASON IS
// TRUE ABOUT THE BYTES AND FALSE ABOUT THE COLUMNS — see linear_csv_dates.go for what is measured
// and what is not.
//
// ⚠ AND IT IS THE TRANSPORT WITH NO GATE IN FRONT OF IT. jira_api/linear_api need
// TRACK_INTEGRATION_ENCRYPTION_KEY, an integration row and a live credential before a single issue
// moves. `linear_csv` is one of exactly two values job_handler.go's validSourceTypes accepts
// UNCONDITIONALLY — an upload, no configuration, no credential. The blocked half got eight merges
// while the half anyone can reach today went unread.
//
// Every assertion goes through ImportLinearCSV, whose signature does not change. linearRowMapper's
// does, and a test written against the mapper would have failed to COMPILE rather than gone red —
// a build error proves the function moved, not that the product was wrong.

// The layout the fixtures FORMAT with, hardcoded rather than read from the package constant: an
// assertion that formats with the same constant the code parses with compares the constant to
// itself and passes for every possible value.
const linearCSVTestDateLayout = "2006-01-02"

// linearCSVHeader is the export header Linear's own importer documentation names, and the one this
// package's fixtures have carried since the first Linear test.
const linearCSVHeader = "ID,Title,Description,Status,Priority,Assignee,Labels,Created,Completed\n"

func importOneLinearRow(t *testing.T, csv string) (model.Issue, *ImportResult) {
	t.Helper()
	imp, store := newTestImporter()
	out, err := imp.ImportLinearCSV(context.Background(), "ws", "team", strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ImportLinearCSV: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("store got %d issues, want 1", len(store.created))
	}
	return store.created[0], out
}

func hasWarningContaining(out *ImportResult, frag string) bool {
	for _, w := range out.Warnings {
		if strings.Contains(w, frag) {
			return true
		}
	}
	return false
}

// ─── Created ────────────────────────────────────────────────

// TestLinearCSV_CreatedLandsOnTheIssue is the column half. It is the SMALLER half: the loss it
// closes is invisible in the column itself, because issues.created_at is TIMESTAMPTZ DEFAULT NOW()
// and an unread Created produces a plausible timestamp rather than a null. The visible half is in
// linear_csv_dates_job_test.go.
func TestLinearCSV_CreatedLandsOnTheIssue(t *testing.T) {
	want := time.Date(2023, 3, 14, 0, 0, 0, 0, time.UTC)
	got, _ := importOneLinearRow(t, linearCSVHeader+
		"LIN-1,Opened long ago,d,Backlog,No priority,,,"+want.Format(linearCSVTestDateLayout)+",\n")

	if !got.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v (the Created column).\n"+
			"A zero CreatedAt takes the column DEFAULT NOW(), so the issue reads as opened at import time.",
			got.CreatedAt, want)
	}
}

// TestLinearCSV_MissingCreatedColumnIsReported is the structural-zero line. Without it an operator
// cannot tell "Track read your Created column" from "Track recorded every one of these as opened
// today" — the two produce identical-looking rows.
func TestLinearCSV_MissingCreatedColumnIsReported(t *testing.T) {
	_, out := importOneLinearRow(t,
		"ID,Title,Description,Status,Priority,Assignee,Labels\nLIN-1,No dates here,d,Backlog,No priority,,\n")

	if !hasWarningContaining(out, `no "Created" column in this export`) {
		t.Errorf("an export with no Created column reported warnings %q; want one naming the absent column.\n"+
			"Every row landed with created_at = the import instant and the job called itself clean.",
			out.Warnings)
	}
}

// TestLinearCSV_EmptyCreatedValueIsReportedApartFromTheMissingColumn keeps the two failures apart:
// an export with no Created column at all is a re-export, and a handful of blank cells is a
// data-quality note about those rows. ci.get answers "" for both, which is why the mapper must take
// the whole columnIndex.
func TestLinearCSV_EmptyCreatedValueIsReportedApartFromTheMissingColumn(t *testing.T) {
	_, out := importOneLinearRow(t, linearCSVHeader+"LIN-1,Blank cell,d,Backlog,No priority,,,,\n")

	if hasWarningContaining(out, `no "Created" column in this export`) {
		t.Errorf("a blank Created CELL was reported as an absent COLUMN: %q", out.Warnings)
	}
	if !hasWarningContaining(out, "empty creation time") {
		t.Errorf("a blank Created cell reported warnings %q; want one naming the empty value", out.Warnings)
	}
}

// TestLinearCSV_UnparseableCreatedIsReportedNotSilentlyDefaulted is the assertion that earns the
// hand-pinned layout list. The list is NOT measured against a real export (see linear_csv_dates.go);
// the refusal is what makes that honest, so a tenant whose serialisation differs learns it on its
// first import instead of receiving a column of import-instant timestamps.
func TestLinearCSV_UnparseableCreatedIsReportedNotSilentlyDefaulted(t *testing.T) {
	got, out := importOneLinearRow(t, linearCSVHeader+
		"LIN-1,Exotic date,d,Backlog,No priority,,,14/03/2023,\n")

	if !got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt = %v for a shape no pinned layout accepts; want the zero value", got.CreatedAt)
	}
	if !hasWarningContaining(out, "not a date shape this importer recognises") {
		t.Errorf("an unrecognised Created shape reported warnings %q; want one naming the value", out.Warnings)
	}
}

// ─── Completed ──────────────────────────────────────────────

// TestLinearCSV_CompletedLandsOnADoneIssue — the column analytics selects on
// (`completed_at IS NOT NULL`). Unread, every Linear CSV import is invisible to the resolution and
// throughput reports however much finished work it carried.
func TestLinearCSV_CompletedLandsOnADoneIssue(t *testing.T) {
	want := time.Date(2023, 6, 30, 0, 0, 0, 0, time.UTC)
	got, _ := importOneLinearRow(t, linearCSVHeader+
		"LIN-2,Finished work,d,Done,Medium,,,2023-03-14,"+want.Format(linearCSVTestDateLayout)+"\n")

	if got.CompletedAt == nil {
		t.Fatalf("CompletedAt = nil for a Done row carrying a Completed value; want %v.\n"+
			"analytics.GetTimeToResolution selects on completed_at IS NOT NULL, so this issue is "+
			"absent from every resolution and throughput report.", want)
	}
	if !got.CompletedAt.Equal(want) {
		t.Errorf("CompletedAt = %v, want %v (the Completed column)", *got.CompletedAt, want)
	}
}

// TestLinearCSV_CompletedIsRefusedAndReportedWhenTheIssueIsNotDone inherits #74's decision rather
// than re-litigating it: Track stamps completed_at only on a transition ONTO done, and analytics'
// resolution query has NO status predicate, so a cancelled issue carrying one is counted as
// delivered work. The refusal is REPORTED, because a deliberate drop nobody is told about is
// indistinguishable from the silent ones this item has found nine times.
func TestLinearCSV_CompletedIsRefusedAndReportedWhenTheIssueIsNotDone(t *testing.T) {
	got, out := importOneLinearRow(t, linearCSVHeader+
		"LIN-3,Abandoned work,d,Cancelled,Medium,,,2023-03-14,2023-06-30\n")

	if got.Status != model.StatusCancelled {
		t.Fatalf("status = %q, want %q — the fixture is not exercising the refusal", got.Status, model.StatusCancelled)
	}
	if got.CompletedAt != nil {
		t.Errorf("CompletedAt = %v on a cancelled issue; want nil — analytics counts it as delivered work",
			*got.CompletedAt)
	}
	if !hasWarningContaining(out, "not recorded") {
		t.Errorf("a refused Completed reported warnings %q; want one saying it was not recorded", out.Warnings)
	}
}

// TestLinearCSV_UnparseableCompletedIsReported — same refusal, on the other column.
func TestLinearCSV_UnparseableCompletedIsReported(t *testing.T) {
	got, out := importOneLinearRow(t, linearCSVHeader+
		"LIN-4,Odd completion,d,Done,Medium,,,2023-03-14,30 June 2023\n")

	if got.CompletedAt != nil {
		t.Errorf("CompletedAt = %v for a shape no pinned layout accepts; want nil", *got.CompletedAt)
	}
	if !hasWarningContaining(out, "not a date shape this importer recognises") {
		t.Errorf("an unrecognised Completed shape reported warnings %q; want one naming the value", out.Warnings)
	}
}

// ─── the other direction ────────────────────────────────────

// TestLinearCSV_AFullyReadableRowAddsNoWarning is one-directional on purpose. Every assertion above
// demands a NEW warning; without this one a mapper that reported something about every row would
// pass all of them, and the warnings channel is the one an operator has to trust for every OTHER
// note kind in this package.
//
// ⚠ IT CARRIES ITS OWN HEADER, NOT linearCSVHeader, AND THAT IS THIS TEST'S SECOND FINDING RATHER
// THAN A CONVENIENCE. linearCSVHeader is the nine columns Linear's IMPORT documentation names, and
// a real EXPORT carries `Updated` too — measured in 44 of 45 real exports and in all six header
// shapes (scripts/w34-linear-csv-updated-probe.py). Once linearRowMapper reads that column, a row
// built from the import header is no longer a row whose every column mapped: Track cannot tell when
// Linear last touched it, and says so. So the fixture that must produce NO warning is the one that
// actually gives the mapper everything it reads. Suppressing the warning instead would have hidden
// the loss this file's own Created argument exists to make audible.
func TestLinearCSV_AFullyReadableRowAddsNoWarning(t *testing.T) {
	_, out := importOneLinearRow(t,
		"ID,Title,Description,Status,Priority,Assignee,Labels,Created,Updated,Completed\n"+
			"LIN-5,Clean row,d,Done,Medium,,bug,2023-03-14,2023-06-29,2023-06-30\n")

	if len(out.Warnings) != 0 {
		t.Errorf("a row whose every column mapped reported warnings %q; want none", out.Warnings)
	}
}

// TestLinearCSV_AnAbsentCompletedColumnIsNotAWarning STATED THE ASYMMETRY "so nobody fixes it
// later", AND HALF OF IT WAS FALSE. It read: "an absent Completed column is an honest NULL that no
// report misreads, and warning about it would put a line an operator cannot act on into the channel
// the Created line needs to be read in" — and its fixture's status was `Done`.
//
// ⚠⚠ BOTH CLAUSES ARE FALSIFIABLE AND BOTH ARE FALSE ON A DONE ISSUE. analytics' resolution and
// throughput queries select on `completed_at IS NOT NULL`, so a done issue with a NULL there is
// dropped from both — it counts as neither open work nor delivered work, which is a report
// misreading it. And the action is the SAME action the Created line asks for: re-export with the
// column. MEASURED over the corpora: 1 of 45 real Linear exports and 7 of 346 real Jira exports
// carry no completion column at all, for 34 and 2,288 done rows respectively.
//
// ⚠ THE OTHER HALF IS TRUE AND IS KEPT, because it is what stops the new line being noise: on an
// OPEN issue an absent Completed column IS an honest NULL, and most rows in every real export are
// open. Both directions are asserted here, so neither can pass by the mapper being stuck.
func TestLinearCSV_AnAbsentCompletedColumnIsAWarningOnlyOnADoneIssue(t *testing.T) {
	t.Run("done — the row analytics drops, and it is now audible", func(t *testing.T) {
		_, out := importOneLinearRow(t,
			"ID,Title,Description,Status,Priority,Assignee,Labels,Created\n"+
				"LIN-6,No completed column,d,Done,Medium,,,2023-03-14\n")

		var got string
		for _, w := range out.Warnings {
			if strings.Contains(w, "Completed") || strings.Contains(w, "completion time") {
				got = w
			}
		}
		if got == "" {
			t.Fatalf("an absent Completed column on a DONE issue reported nothing; warnings = %q", out.Warnings)
		}
		if strings.Contains(got, "Resolved") {
			t.Errorf("the Linear line names Jira's column: %q", got)
		}
	})
	t.Run("open — still silent, which is what keeps the line above readable", func(t *testing.T) {
		_, out := importOneLinearRow(t,
			"ID,Title,Description,Status,Priority,Assignee,Labels,Created\n"+
				"LIN-7,No completed column,d,In Progress,Medium,,,2023-03-14\n")

		for _, w := range out.Warnings {
			if strings.Contains(w, "Completed") || strings.Contains(w, "completion time") {
				t.Errorf("an OPEN issue produced %q; a NULL completion time on open work is truthful", w)
			}
		}
	})
}
