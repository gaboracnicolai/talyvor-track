package importer_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// linear_csv_due_date_job_test.go — the `Due Date` column of a Linear CSV export, driven END TO END
// through the async runner onto real Postgres.
//
// ⚠ WHY A MAPPER TEST IS NOT ENOUGH HERE, and it is not the usual reason. linearRowMapper returning
// a *time.Time proves the MAPPER reads the column; it cannot see what the write path does with it,
// and this write path has an explicit opinion. issue.Store.UpsertByIdentifier CLOBBERS title,
// description and labels on re-import and OMITS due_date from that list on purpose ("whether a
// provider's plan should overwrite a local one is a decision, not a default"). A row carrying an
// `ID` — which 45 of 45 real exports do — travels the upsert route, so the mapper's value only ever
// reaches the column through the INSERT branch. That branch had never once been exercised with a
// non-nil due date on this transport, because no due date ever arrived.
//
// ⚠ THE FIXTURE HEADER IS THE ONE THE CENSUS MEASURED, NOT THE ONE THIS PACKAGE'S OTHER LINEAR
// FIXTURES CARRY. The nine-column header the rest of the Linear tests use is Linear's IMPORT
// documentation header and it has no `Due Date` in it — which is exactly how the column stayed
// invisible. A fixture that cannot carry the value cannot fail when the value is dropped.
const (
	// HARDCODED as the shape the census MEASURED (408 of 447 real cells, e.g.
	// `2026-06-15T15:03:28.558Z`) rather than read back from linearCSVTimeLayouts. A fixture that
	// formats with the same constant the code parses with compares the constant to itself and
	// passes for every possible value. The trailing Z is literal layout text, not Go's zone form,
	// which is what keeps this string independent of time.RFC3339.
	linearCSVDueDateTestLayout = "2006-01-02T15:04:05.000Z"

	// Computed rather than written down, for the reason #83's fixture is: a hardcoded date ages
	// out and the test stops testing anything while staying green.
	linearCSVDueDateDaysAhead = 30
)

// linearCSVDueDateFixture returns a one-row export due `days` from now. `ID` is carried so the row
// travels the upsert route a real export takes rather than the keyless Create route.
func linearCSVDueDateFixture(days int) (csv string, due time.Time) {
	due = time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Truncate(time.Millisecond)
	csv = "ID,Team,Title,Description,Status,Priority,Labels,Created,Updated,Completed,Due Date\n" +
		fmt.Sprintf("LIN-1,Eng,widget with a deadline,d,In Progress,High,,,,,%s\n",
			due.Format(linearCSVDueDateTestLayout))
	return csv, due
}

func runLinearCSVDueDateImport(t *testing.T, d *testutil.DB, wsID, teamID, body string) {
	t.Helper()
	ctx := context.Background()
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	if _, err := js.Create(ctx, wsID, teamID, "linear_csv", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
}

// TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearSaidItWasDue — the column half.
func TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearSaidItWasDue(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	body, due := linearCSVDueDateFixture(linearCSVDueDateDaysAhead)
	runLinearCSVDueDateImport(t, d, ws.ID, team.ID, body)

	var got *time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT due_date FROM issues WHERE workspace_id = $1`, ws.ID).Scan(&got); err != nil {
		t.Fatalf("read due_date: %v", err)
	}
	if got == nil {
		t.Fatalf("due_date IS NULL, want %s (the %q column).\n"+
			"jira_csv, jira_api and linear_api have all read a due date since #73/#74; linear_csv "+
			"was the only transport of the four that dropped it, and the column is in 45 of 45 "+
			"real Linear exports.", due.Format(time.RFC3339), "Due Date")
	}
	if delta := got.UTC().Sub(due); delta > time.Minute || delta < -time.Minute {
		t.Errorf("due_date = %s, want %s — off by %s",
			got.UTC().Format(time.RFC3339), due.Format(time.RFC3339), delta)
	}
}

// TestJobRow_LinearCSV_ARepeatedImportDoesNotMoveTheDueDate — the branch the first test cannot
// reach, and the limit this merge deliberately does NOT change.
//
// ⚠ THIS IS AN INHERITED DECISION BEING MADE OBSERVABLE, NOT A DEFECT BEING FIXED.
// UpsertByIdentifier omits due_date from its clobber list on purpose, and says the choice "is
// stated in the queue rather than made here". The consequence, now that a due date can actually
// arrive on this transport: THE FIRST IMPORT IS THE ONLY ONE THAT CAN EVER SET ONE. Move a
// deadline in Linear, re-import, and Track keeps the old date while clobbering title, description
// and labels from the same row — so the row is half-refreshed and nothing says so.
//
// It is asserted rather than left implicit because a merge that made due dates arrive is exactly
// when someone would "obviously" add due_date to the CLOBBER list, and that is a product decision
// about whose plan wins. If this test goes red, the decision was made — check it was made on
// purpose.
func TestJobRow_LinearCSV_ARepeatedImportDoesNotMoveTheDueDate(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	first, firstDue := linearCSVDueDateFixture(linearCSVDueDateDaysAhead)
	runLinearCSVDueDateImport(t, d, ws.ID, team.ID, first)

	// The SAME issue (same ID ⇒ the upsert route), with the deadline moved a long way out and the
	// title changed. The title is the control: it is in the CLOBBER list, so if it does not move
	// the row never took the UPDATE branch at all and the due-date assertion below would be
	// measuring a second INSERT instead of a re-import.
	second, secondDue := linearCSVDueDateFixture(linearCSVDueDateDaysAhead + 90)
	second = replaceOnce(t, second, "widget with a deadline", "widget with a MOVED deadline")
	runLinearCSVDueDateImport(t, d, ws.ID, team.ID, second)

	var n int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE workspace_id = $1`, ws.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d issues after two imports of the same ID, want 1 — the row did not upsert, so "+
			"nothing below is about a re-import", n)
	}

	var title string
	var got *time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT title, due_date FROM issues WHERE workspace_id = $1`, ws.ID).Scan(&title, &got); err != nil {
		t.Fatal(err)
	}
	// The floor: prove the UPDATE branch actually ran. Without this the due-date assertion is
	// satisfied by a re-import that did nothing whatsoever.
	if title != "widget with a MOVED deadline" {
		t.Fatalf("title = %q — the clobbered column did not move, so the UPDATE branch never ran "+
			"and the due-date assertion below is vacuous", title)
	}
	if got == nil {
		t.Fatalf("due_date IS NULL after a re-import — the first import's value was ERASED, which " +
			"is neither the clobber behaviour nor the preserve behaviour")
	}
	if delta := got.UTC().Sub(firstDue); delta > time.Minute || delta < -time.Minute {
		t.Errorf("due_date = %s after re-import; want the FIRST import's %s (preserved), not the "+
			"second's %s.\nIf this changed on purpose, due_date moved into UpsertByIdentifier's "+
			"CLOBBER list and that is a decision about whose plan wins — the provider's or the "+
			"user's. See internal/issue/store.go's OMITTED comment.",
			got.UTC().Format(time.RFC3339), firstDue.Format(time.RFC3339), secondDue.Format(time.RFC3339))
	}
}

// replaceOnce fails the test if the needle is not present exactly once — a silent no-op here would
// turn the re-import control above into a comparison of two identical rows.
func replaceOnce(t *testing.T, s, old, replacement string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("fixture edit: %q appears %d times, want exactly 1", old, n)
	}
	return strings.Replace(s, old, replacement, 1)
}
