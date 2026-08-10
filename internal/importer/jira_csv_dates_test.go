package importer

import (
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/model"
)

// jira_csv_dates_test.go — the two dates a real Jira CSV EXPORT carries and jiraRowMapper never read.
//
// ⚠ WHY THIS TRANSPORT AND NOT THE API ONE AGAIN. #74 taught the jira_api source `duedate` and
// `resolutiondate`; #77 did the same for linear_api. Both were shipped against a provider nobody in
// this environment can authenticate to — W3.4 item (3), still open: NO `*_api` IMPORT HAS EVER BEEN
// PROVEN END TO END AGAINST A REAL TENANT. The CSV transport needs no credentials at all, so it is
// the half of this feature a customer can actually run today, and it maps exactly FIVE fields:
// title, description, status, priority, labels. A Jira CSV export's Due Date and Resolved columns
// went into the same silence #71/#72/#73/#74/#77 each found one field over — data loss reported as
// {imported:N, skipped:0, warnings:[]}.
//
// MEASURED AGAINST A REAL JIRA CSV EXPORT, not reasoned about (jira.atlassian.com, anonymous, the
// issue-navigator CSV view, 2026-08-09). NEGATIVE-CONTROLLED FIRST so a 200 is not a blanket answer:
// a fabricated host resolved to nothing (curl 000), a fabricated VIEW name on the real host answered
// 400 text/html, and a fabricated PROJECT in the JQL answered 400 text/html — only the real request
// returned 200 text/csv. scripts/w34-jira-csv-export-probe.py re-runs the whole measurement.
//
//	header    carries EXACTLY these two spellings: "Due Date" and "Resolved" (the column COUNT
//	          is deliberately not pinned — an all-fields export repeats multi-value columns per
//	          row, and the same view answered 212 and 279 for two different result sets)
//	Due Date  "19/Jan/2025 12:00 AM"   ← always a midnight local time, never a bare date
//	Resolved  "25/Mar/2025 10:03 AM"
//	Created   "07/Aug/2026 12:54 PM" · "09/Aug/2026 8:15 AM"  ← the hour is NOT zero-padded
//
// ⚠ AND THE TRAP, WHICH IS #74's ONE TRANSPORT OVER: none of the three layouts this package already
// pins accepts ANY of those bytes. time.RFC3339, "2006-01-02" and the API's
// "2006-01-02T15:04:05.000-0700" all REFUSE "19/Jan/2025 12:00 AM" — proven by running them, and
// pinned below in TestJiraCSVTime_TheAPILayoutsRefuseEveryMeasuredCSVDate. An implementation that
// reached for the neighbouring helper would have written nil into every row of both columns while
// every RFC3339-shaped fixture in this package kept passing.

// The bytes above, verbatim, as the fixtures for everything in this file. Named so a reader can tell
// a MEASURED value from an invented one at the call site.
const (
	realJiraCSVDueDate    = "19/Jan/2025 12:00 AM"
	realJiraCSVResolved   = "25/Mar/2025 10:03 AM"
	realJiraCSVCreated    = "07/Aug/2026 12:54 PM"
	realJiraCSVSingleHour = "09/Aug/2026 8:15 AM"
)

// mapOneJiraCSVRow builds a one-row CSV with the given header and runs the SHIPPED mapper over it,
// through buildIndex — so the column-name lookup is exercised, not bypassed.
func mapOneJiraCSVRow(t *testing.T, header, row []string) mappedIssue {
	t.Helper()
	ci := buildIndex(header)
	got, err := jiraRowMapper(ci, row)
	if err != nil {
		t.Fatalf("jiraRowMapper: %v", err)
	}
	return got
}

func wantUTC(t *testing.T, rfc3339 string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatalf("bad want %q: %v", rfc3339, err)
	}
	return v.UTC()
}

// ─── the mapper ─────────────────────────────────────────────

// A Due Date column in the shape a real export emits reaches model.Issue.DueDate.
func TestJiraCSVMapper_CapturesTheDueDateColumn(t *testing.T) {
	// The header spellings are HARDCODED here, never read from a constant the mapper also reads.
	// #75's C6: a guard that compares the constant to itself passes for every possible value.
	got := mapOneJiraCSVRow(t,
		[]string{"Summary", "Status", "Priority", "Due Date", "Created", "Updated"},
		[]string{"Ship it", "To Do", "High", realJiraCSVDueDate, "23/Jul/2026 7:36 PM", "23/Jul/2026 7:36 PM"})

	if got.issue.DueDate == nil {
		t.Fatalf("DueDate is nil; the export column %q held %q", "Due Date", realJiraCSVDueDate)
	}
	if want := wantUTC(t, "2025-01-19T00:00:00Z"); !got.issue.DueDate.Equal(want) {
		t.Errorf("DueDate = %s, want %s", got.issue.DueDate.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if len(got.notes) != 0 {
		t.Errorf("a date that parsed must produce no note; got %+v", got.notes)
	}
}

// A Resolved column on a row that imports as done reaches model.Issue.CompletedAt.
func TestJiraCSVMapper_CapturesResolvedOnADoneRow(t *testing.T) {
	got := mapOneJiraCSVRow(t,
		[]string{"Summary", "Status", "Priority", "Resolved"},
		[]string{"Shipped", "Closed", "High", realJiraCSVResolved})

	if got.issue.Status != model.StatusDone {
		t.Fatalf("precondition: status = %q, want %q (Jira's \"Closed\")", got.issue.Status, model.StatusDone)
	}
	if got.issue.CompletedAt == nil {
		t.Fatalf("CompletedAt is nil; the export column %q held %q on a done row", "Resolved", realJiraCSVResolved)
	}
	if want := wantUTC(t, "2025-03-25T10:03:00Z"); !got.issue.CompletedAt.Equal(want) {
		t.Errorf("CompletedAt = %s, want %s", got.issue.CompletedAt.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// #74's DECISION, INHERITED RATHER THAN RE-LITIGATED: CompletedAt means FINISHED, not "left the
// board". Jira resolves "Won't Do" too, so a cancelled issue carries a Resolved date — and Track's
// issue.Store.Update stamps completed_at only on a transition ONTO done and clears it on any
// transition away, while analytics' resolution-stats query selects on `completed_at IS NOT NULL`
// with NO status predicate. A non-done row carrying one is counted as delivered work.
//
// The refusal is REPORTED. A deliberate drop nobody is told about is byte-identical to the silent
// ones this item has now found six times.
func TestJiraCSVMapper_RefusesAndReportsResolvedOnANonDoneRow(t *testing.T) {
	got := mapOneJiraCSVRow(t,
		[]string{"Summary", "Status", "Priority", "Resolved"},
		[]string{"Abandoned", "Won't Do", "High", realJiraCSVResolved})

	if got.issue.Status != model.StatusCancelled {
		t.Fatalf("precondition: status = %q, want %q", got.issue.Status, model.StatusCancelled)
	}
	if got.issue.CompletedAt != nil {
		t.Errorf("CompletedAt = %v on a %s row", got.issue.CompletedAt, got.issue.Status)
	}
	line := renderOnly(t, got.notes, fieldResolutionDate)
	for _, want := range []string{realJiraCSVResolved, "not recorded", string(model.StatusCancelled)} {
		if !strings.Contains(line, want) {
			t.Errorf("the refusal must name %q; line = %q", want, line)
		}
	}
}

// A value that ARRIVED AND WAS REFUSED must not look like a value that never arrived. This is the
// whole reason the layouts may be pinned by hand: the per-instance date format is a Jira LOOK-AND-FEEL
// PREFERENCE, so a tenant whose export differs from the measured shape learns it on its first import
// instead of receiving a column of nulls that reads as "we have no due dates".
func TestJiraCSVMapper_ReportsADateShapeItCannotParse(t *testing.T) {
	const exotic = "2025-01-19" // a perfectly real date, in a shape this export does NOT emit
	got := mapOneJiraCSVRow(t,
		[]string{"Summary", "Status", "Priority", "Due Date"},
		[]string{"Ship it", "To Do", "High", exotic})

	if got.issue.DueDate != nil {
		t.Errorf("DueDate = %v; %q is not a shape pinned from the measurement", got.issue.DueDate, exotic)
	}
	line := renderOnly(t, got.notes, fieldDueDate)
	for _, want := range []string{exotic, "not a date shape", "not recorded"} {
		if !strings.Contains(line, want) {
			t.Errorf("the refusal must name %q; line = %q", want, line)
		}
	}
}

// The other direction, so neither column can pass by always being full: an export with no such
// column, and an export whose column is empty, both produce a nil and NO note. An absent due date is
// not a loss and must not be reported as one — that is what turns a warnings channel into noise.
func TestJiraCSVMapper_AbsentDatesAreSilentAndNil(t *testing.T) {
	t.Run("column missing entirely", func(t *testing.T) {
		got := mapOneJiraCSVRow(t,
			// ⚠ Created is supplied because "absent column, no loss" does NOT hold for it: see the
			// note on TestJobRow_JiraCSV_NoDateColumnsIsCleanAndSilent. This subtest is about the
			// two columns #78 shipped.
			[]string{"Summary", "Status", "Priority", "Created", "Updated"},
			[]string{"Ship it", "To Do", "High", "23/Jul/2026 7:36 PM", "23/Jul/2026 7:36 PM"})
		if got.issue.DueDate != nil || got.issue.CompletedAt != nil {
			t.Errorf("dates = {%v %v}, want both nil", got.issue.DueDate, got.issue.CompletedAt)
		}
		if len(got.notes) != 0 {
			t.Errorf("notes = %+v, want none", got.notes)
		}
	})
	t.Run("column present and empty", func(t *testing.T) {
		got := mapOneJiraCSVRow(t,
			[]string{"Summary", "Status", "Priority", "Due Date", "Resolved", "Created", "Updated"},
			[]string{"Ship it", "Closed", "High", "", "", "23/Jul/2026 7:36 PM", "23/Jul/2026 7:36 PM"})
		if got.issue.DueDate != nil || got.issue.CompletedAt != nil {
			t.Errorf("dates = {%v %v}, want both nil", got.issue.DueDate, got.issue.CompletedAt)
		}
		if len(got.notes) != 0 {
			t.Errorf("notes = %+v, want none", got.notes)
		}
	})
}

// ─── the layouts, and the provenance that made them separate ─────────────────

// Rule 2's twin: the MEASURED bytes, pinned by hand, must parse — and to the instant, not merely
// without an error. A layout list is prose until something asserts what it produces.
func TestJiraCSVTime_ParsesEveryMeasuredShape(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{realJiraCSVDueDate, "2025-01-19T00:00:00Z"},
		{realJiraCSVResolved, "2025-03-25T10:03:00Z"},
		{realJiraCSVCreated, "2026-08-07T12:54:00Z"},
		{realJiraCSVSingleHour, "2026-08-09T08:15:00Z"},
	} {
		got, ok := parseJiraCSVTime(tc.in)
		if !ok {
			t.Errorf("%q REFUSED — it is a byte-for-byte value a real Jira CSV export emitted", tc.in)
			continue
		}
		if s := got.UTC().Format(time.RFC3339); s != tc.want {
			t.Errorf("%q => %s, want %s", tc.in, s, tc.want)
		}
	}
}

// ⚠ THE CONTROL THAT DECIDED THE DESIGN, AND IT IS A TEST RATHER THAN A COMMENT. Reusing the
// neighbouring parseJiraTime would have lent #74's OBSERVED-BYTES provenance to a transport whose
// bytes are a different shape entirely — the overclaim #75 caught in this package once already. It
// would also simply not have worked, and this is the assertion that says so out loud.
func TestJiraCSVTime_TheAPILayoutsRefuseEveryMeasuredCSVDate(t *testing.T) {
	for _, v := range []string{realJiraCSVDueDate, realJiraCSVResolved, realJiraCSVCreated, realJiraCSVSingleHour} {
		if _, ok := parseJiraTime(v); ok {
			t.Errorf("parseJiraTime accepted %q — if the API layouts ever cover the CSV shape, these two "+
				"helpers should be re-argued deliberately, not merged by accident", v)
		}
		// The single constant everyone reaches for first, named explicitly so the refusal is on the record.
		if _, err := time.Parse(time.RFC3339, v); err == nil {
			t.Errorf("time.RFC3339 accepted %q", v)
		}
	}
	// ...and the reverse, so this is not a one-directional claim: the CSV layouts must NOT quietly
	// swallow the API's shapes either. Two provenances, two lists, and neither pretends to be the other.
	for _, v := range []string{"2027-12-31", "2026-08-06T20:06:39.000+0000"} {
		if _, ok := parseJiraCSVTime(v); ok {
			t.Errorf("parseJiraCSVTime accepted the API shape %q", v)
		}
	}
}

// A hand-pinned list is only honest if it is also SMALL and STATED. Both directions, so a layout
// added without a measurement behind it fails here until somebody writes down where the bytes came
// from, and a layout deleted fails as stale.
func TestJiraCSVTime_TheLayoutListIsExactlyWhatWasMeasured(t *testing.T) {
	want := []string{"2/Jan/2006 3:04 PM"}
	if len(jiraCSVTimeLayouts) != len(want) {
		t.Fatalf("jiraCSVTimeLayouts = %q, want %q — every entry needs measured bytes behind it "+
			"(see scripts/w34-jira-csv-export-probe.py)", jiraCSVTimeLayouts, want)
	}
	for i := range want {
		if jiraCSVTimeLayouts[i] != want[i] {
			t.Errorf("layout %d = %q, want %q", i, jiraCSVTimeLayouts[i], want[i])
		}
	}
}

// ─── the column names, pinned as a wire contract ─────────────────

// #75's argument, one transport over. The mapper's authority over which column it reads is the
// STRING, and before this file nothing in the package named either spelling — so "Due Date" could
// have become "DueDate", or "Duedate", or "", and the whole suite would have stayed green because
// every fixture supplies its own header. The literals are hardcoded on purpose.
//
// ⚠ IT IS A REAL RISK AND NOT A HYPOTHETICAL ONE: Jira's export column headers are the FIELD's
// display name, and the two this merge reads are spelled with a space and a capital in the measured
// export. A mapper looking up "duedate" finds nothing and reports nothing.
func TestJiraCSVColumns_TheMeasuredSpellingsAreWhatTheMapperReads(t *testing.T) {
	// Exactly the header a real export emitted, reduced to the columns under test.
	header := []string{"Summary", "Status", "Priority", "Due Date", "Resolved"}
	row := []string{"Shipped", "Closed", "High", realJiraCSVDueDate, realJiraCSVResolved}
	got := mapOneJiraCSVRow(t, header, row)
	if got.issue.DueDate == nil || got.issue.CompletedAt == nil {
		t.Fatalf("the measured spellings did not both reach the model: due=%v completed=%v",
			got.issue.DueDate, got.issue.CompletedAt)
	}

	// And the columns are matched case-INSENSITIVELY, which buildIndex already promises and nothing
	// asserted: Jira's own docs render the field as "Due date" in places.
	lower := mapOneJiraCSVRow(t,
		[]string{"summary", "status", "priority", "due date", "resolved"},
		row)
	if lower.issue.DueDate == nil || lower.issue.CompletedAt == nil {
		t.Errorf("lowercased headers did not reach the model: due=%v completed=%v",
			lower.issue.DueDate, lower.issue.CompletedAt)
	}

}

// The NEGATIVE half, deliberately its OWN test function rather than a third block in the one above.
// ⚠ THE SPLIT IS NOT TIDINESS — IT IS WHAT MAKES THE CONTROL READABLE. When the two halves shared a
// function, the mutation that points the mapper at a neighbouring date column tripped the POSITIVE
// half's t.Fatalf first, so the control observed a red that never reached this assertion at all: a
// catch scored for the wrong reason, which is #76's C1 arriving through my own harness. Measured,
// not reasoned about — the campaign printed "NOT A CLEAN CATCH" and this is the fix.
//
// Without it, every assertion above is satisfied by a mapper that reads any column whose name merely
// contains "date" — and a real export has "Custom field (Target Release Date)" sitting right there.
func TestJiraCSVColumns_ANeighbouringDateColumnIsNotRead(t *testing.T) {
	other := mapOneJiraCSVRow(t,
		[]string{"Summary", "Status", "Priority", "Custom field (Target Release Date)", "Created"},
		[]string{"Shipped", "Closed", "High", realJiraCSVDueDate, realJiraCSVCreated})
	if other.issue.DueDate != nil {
		t.Errorf("DueDate = %v — read out of a column that is not %q", other.issue.DueDate, "Due Date")
	}
	if other.issue.CompletedAt != nil {
		t.Errorf("CompletedAt = %v — read out of a column that is not %q", other.issue.CompletedAt, "Resolved")
	}
}

// ⚠ THE LINEAR CSV HALF IS DELIBERATELY UNTOUCHED, AND THIS TEST IS WHY IT CANNOT DRIFT SHUT
// QUIETLY. Linear's CSV export is produced in-app behind authentication; nothing in this
// environment can fetch one, so its column spellings and its date serialisation are UNMEASURED.
// #75 caught this exact package lending one provider's provenance to another once already, and
// guessing "Due Date"/"Completed" for Linear would be the same move. linearRowMapper therefore still
// maps five fields, and this pins that as a KNOWN GAP rather than an oversight — whoever gets a real
// Linear export deletes this test and does the #74 merge on the other transport.
func TestLinearCSVMapper_StillReadsNoDates_AKnownUnmeasuredGap(t *testing.T) {
	ci := buildIndex([]string{"Title", "Status", "Priority", "Due Date", "Completed"})
	got, err := linearRowMapper(ci, []string{"Ship it", "Done", "High", "2025-01-19", "2025-03-25"})
	if err != nil {
		t.Fatal(err)
	}
	if got.issue.DueDate != nil || got.issue.CompletedAt != nil {
		t.Fatalf("linearRowMapper now reads dates (due=%v completed=%v) — if that was deliberate, this "+
			"test should have been deleted along with the reason above, and the column spellings and "+
			"date shape need a measurement behind them",
			got.issue.DueDate, got.issue.CompletedAt)
	}
}

// renderOnly finds the note for one field and renders it, failing if there is not exactly one.
func renderOnly(t *testing.T, notes []FieldNote, field string) string {
	t.Helper()
	var found []FieldNote
	for _, n := range notes {
		if n.Field == field {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		t.Fatalf("notes for %q = %d, want exactly 1; all notes = %+v", field, len(found), notes)
	}
	return found[0].render(1)
}
