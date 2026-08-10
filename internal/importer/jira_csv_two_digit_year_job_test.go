package importer_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// jira_csv_two_digit_year_job_test.go — the SAME end-to-end path jira_csv_created_job_test.go
// drives, with ONE byte-level difference: the export renders its years with TWO digits.
//
// ⚠ WHY THAT ONE DIFFERENCE IS WORTH ITS OWN FILE. jiraCSVTimeLayouts held exactly one layout,
// `2/Jan/2006 3:04 PM`, pinned from the bytes of ONE real export. Censused against the 301 real
// Jira CSV exports already cached for this package's status-category and priority work — the same
// corpus, a question nobody had asked of it — that layout REFUSES 37,255 of 40,523 date cells
// (91.9%):
//
//	Created    accepted=1634  refused=15418  (90.4%)  296 of 299 files lose EVERY cell
//	Updated    accepted=1634  refused=13738  (89.4%)  290 of 293 files lose EVERY cell
//	Due Date   accepted=0     refused=2910   (100%)   119 of 119 files lose EVERY cell
//	Resolved   accepted=0     refused=5189   (100%)   178 of 178 files lose EVERY cell
//
// The single most common real shape is `24/Jun/26 5:39 PM` — 15,915 cells — and the second is
// `05/Jun/26 11:20 PM` — 10,998. Both are the two-digit-year rendering of the layout already
// pinned. 298 of 301 real exports carry at least one refused date cell.
//
// ⚠ AND THE TWO LOSSES COMPOUND INTO SILENCE, which is why this asserts through the REPORT and not
// only the column. Created refused ⇒ created_at falls to its DEFAULT NOW() — a plausible instant,
// not a null, so nothing looks wrong. Resolved refused ⇒ completed_at stays NULL ⇒ the row drops
// out of GetTimeToResolution's `completed_at IS NOT NULL` filter entirely. Separately each is loud;
// together the issue simply is not in the report, and an empty report reads as "no resolved work"
// rather than as a parse failure.
const (
	// HARDCODED, not read from the package's layout list. #75's C6: an assertion that formats with
	// the same constant the code parses with compares the constant to itself and passes for every
	// possible value — including for a list this test exists to prove is incomplete.
	jiraCSVTwoDigitYearLayout = "2/Jan/06 3:04 PM"

	jiraCSVTDYCreatedDaysAgo  = 200
	jiraCSVTDYResolvedDaysAgo = 100
	jiraCSVTDYTrueCycleHours  = float64(jiraCSVTDYCreatedDaysAgo-jiraCSVTDYResolvedDaysAgo) * 24
)

// jiraCSVTwoDigitYearFixture mirrors jiraCSVCreatedFixture exactly, EXCEPT for the year width. The
// dates are computed rather than written down for that function's stated reason: both analytics
// queries filter on `created_at > NOW() - INTERVAL '1 day' * $2`, so a hardcoded date would age out
// of the window and the test would stop testing anything while staying green.
func jiraCSVTwoDigitYearFixture() (body string, created, resolved, due time.Time) {
	now := time.Now().UTC()
	created = now.Add(-time.Duration(jiraCSVTDYCreatedDaysAgo) * 24 * time.Hour).Truncate(time.Minute)
	resolved = now.Add(-time.Duration(jiraCSVTDYResolvedDaysAgo) * 24 * time.Hour).Truncate(time.Minute)
	due = now.Add(-time.Duration(jiraCSVTDYResolvedDaysAgo+10) * 24 * time.Hour).Truncate(time.Minute)
	body = "Summary,Description,Status,Priority,Resolution,Created,Resolved,Due Date\n" +
		fmt.Sprintf("Opened long before the import,d,Closed,High,Fixed,%s,%s,%s\n",
			created.Format(jiraCSVTwoDigitYearLayout),
			resolved.Format(jiraCSVTwoDigitYearLayout),
			due.Format(jiraCSVTwoDigitYearLayout))
	return body, created, resolved, due
}

func runJiraCSVTwoDigitYearImport(t *testing.T, d *testutil.DB, body string) string {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	if _, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return ws.ID
}

// TestJobRow_JiraCSVTwoDigitYear_KeepsTheDateJiraOpenedIt is the column half.
func TestJobRow_JiraCSVTwoDigitYear_KeepsTheDateJiraOpenedIt(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, created, _, _ := jiraCSVTwoDigitYearFixture()
	wsID := runJiraCSVTwoDigitYearImport(t, d, body)

	var got time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT created_at FROM issues WHERE workspace_id = $1`, wsID).Scan(&got); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if delta := got.UTC().Sub(created); delta > time.Minute || delta < -time.Minute {
		t.Errorf("created_at = %s, want %s (the Created column, two-digit year) — off by %s.\n"+
			"A defaulted created_at is the import instant, so every imported issue reads as opened today.",
			got.UTC().Format(time.RFC3339), created.Format(time.RFC3339), delta)
	}
}

// TestJobRow_JiraCSVTwoDigitYear_ResolvedAndDueLand is the pair the column half cannot infer:
// completed_at and due_date are the two the analytics surfaces select on.
func TestJobRow_JiraCSVTwoDigitYear_ResolvedAndDueLand(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, _, resolved, due := jiraCSVTwoDigitYearFixture()
	wsID := runJiraCSVTwoDigitYearImport(t, d, body)

	var gotCompleted, gotDue *time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT completed_at, due_date FROM issues WHERE workspace_id = $1`, wsID).
		Scan(&gotCompleted, &gotDue); err != nil {
		t.Fatalf("read completed_at/due_date: %v", err)
	}
	if gotCompleted == nil {
		t.Errorf("completed_at is NULL for a row whose Resolved column said %s — a refused "+
			"resolution date drops the issue out of every report that filters on completed_at.",
			resolved.Format(time.RFC3339))
	} else if delta := gotCompleted.UTC().Sub(resolved); delta > time.Minute || delta < -time.Minute {
		t.Errorf("completed_at = %s, want %s — off by %s",
			gotCompleted.UTC().Format(time.RFC3339), resolved.Format(time.RFC3339), delta)
	}
	if gotDue == nil {
		t.Errorf("due_date is NULL for a row whose Due Date column said %s — 119 of 119 real "+
			"exports carrying the column lose every cell of it.", due.Format(time.RFC3339))
	} else if delta := gotDue.UTC().Sub(due); delta > time.Minute || delta < -time.Minute {
		t.Errorf("due_date = %s, want %s — off by %s",
			gotDue.UTC().Format(time.RFC3339), due.Format(time.RFC3339), delta)
	}
}

// TestJobRow_JiraCSVTwoDigitYear_CycleTimeIsReported is the half a column read cannot do, and the
// one that shows the compound silence: with BOTH dates refused the issue is not merely wrong in
// the report, it is absent from it.
func TestJobRow_JiraCSVTwoDigitYear_CycleTimeIsReported(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, _, _, _ := jiraCSVTwoDigitYearFixture()
	wsID := runJiraCSVTwoDigitYearImport(t, d, body)

	stats, err := analytics.New(d.Pool).GetTimeToResolution(ctx, wsID, "", 365)
	if err != nil {
		t.Fatalf("resolution stats: %v", err)
	}
	if stats.MedianHours <= 0 {
		t.Errorf("median time to resolution = %.1f hours for an issue Jira opened %d days ago and "+
			"finished %d days ago (two-digit-year export).\nZero means the row never reached the "+
			"report: a refused Resolved leaves completed_at NULL and the `completed_at IS NOT NULL` "+
			"filter drops it.", stats.MedianHours, jiraCSVTDYCreatedDaysAgo, jiraCSVTDYResolvedDaysAgo)
	}
	if delta := stats.MedianHours - jiraCSVTDYTrueCycleHours; delta > 24 || delta < -24 {
		t.Errorf("median time to resolution = %.1f hours, want ≈ %.0f (Resolved − Created)",
			stats.MedianHours, jiraCSVTDYTrueCycleHours)
	}
}
