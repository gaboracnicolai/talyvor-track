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

// jira_csv_created_job_test.go — the `Created` column driven END TO END on real Postgres and then
// READ BACK THROUGH THE REPORT THAT CONSUMES IT.
//
// ⚠ WHY THIS TEST REACHES INTO internal/analytics AND WHY THAT IS NOT SCOPE CREEP. Every other date
// this item has landed is verifiable as a column: #74's asymmetry note says a landed date is
// DIRECTLY OBSERVABLE as a non-null value, so "the code never ran" and "the provider sent none" are
// distinguishable by querying. `created_at` is NOT like that. Postgres DEFAULTs it, so the column is
// ALWAYS non-null and ALWAYS looks populated — the wrong value and the right value have identical
// shape. The only place the difference is observable is the number computed FROM it:
//
//	analytics.GetTimeToResolution ⇒ EXTRACT(EPOCH FROM completed_at - created_at)/3600
//
// #74 and #78 deliberately landed `completed_at` from the provider. With `created_at` defaulted to
// the import instant, that subtraction is (a past time) − (now), which is NEGATIVE. A column test
// cannot see that; this one can.
//
// The window matters and is the reason the fixture dates are COMPUTED rather than written down: both
// analytics queries filter `created_at > NOW() - INTERVAL '1 day' * $2` with $2 clamped to 365, so a
// hardcoded date would silently age out of the window and the test would stop testing anything while
// staying green.
const (
	// The measured export layout, HARDCODED rather than read from the package constant. #75's C6:
	// an assertion that formats with the same constant the code parses with compares the constant to
	// itself and passes for every possible value.
	jiraCSVCreatedTestLayout = "2/Jan/2006 3:04 PM"

	// 200 days ago opened, 100 days ago finished ⇒ a TRUE cycle time of 100 days = 2400 hours, and
	// both instants inside the 365-day analytics window.
	jiraCSVCreatedDaysAgo  = 200
	jiraCSVResolvedDaysAgo = 100
	jiraCSVTrueCycleHours  = float64(jiraCSVCreatedDaysAgo-jiraCSVResolvedDaysAgo) * 24
)

func jiraCSVCreatedFixture() (csv string, created, resolved time.Time) {
	now := time.Now().UTC()
	// Truncate to the minute: the layout carries no seconds, so a round-trip through it loses them
	// and an equality assertion would fail for a reason that has nothing to do with the finding.
	created = now.Add(-time.Duration(jiraCSVCreatedDaysAgo) * 24 * time.Hour).Truncate(time.Minute)
	resolved = now.Add(-time.Duration(jiraCSVResolvedDaysAgo) * 24 * time.Hour).Truncate(time.Minute)
	csv = "Summary,Description,Status,Priority,Resolution,Created,Resolved\n" +
		fmt.Sprintf("Opened long before the import,d,Closed,High,Fixed,%s,%s\n",
			created.Format(jiraCSVCreatedTestLayout), resolved.Format(jiraCSVCreatedTestLayout))
	return csv, created, resolved
}

func runJiraCSVCreatedImport(t *testing.T, d *testutil.DB, body string) (wsID string) {
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

// TestJobRow_JiraCSV_ImportedIssueKeepsTheDateJiraOpenedIt is the column half: the row must carry
// the instant the PROVIDER opened the issue, not the instant the import ran.
func TestJobRow_JiraCSV_ImportedIssueKeepsTheDateJiraOpenedIt(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, created, _ := jiraCSVCreatedFixture()
	wsID := runJiraCSVCreatedImport(t, d, body)

	var got time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT created_at FROM issues WHERE workspace_id = $1`, wsID).Scan(&got); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if delta := got.UTC().Sub(created); delta > time.Minute || delta < -time.Minute {
		t.Errorf("created_at = %s, want %s (the Created column) — off by %s.\n"+
			"A defaulted created_at is the import instant, so every imported issue reads as opened today.",
			got.UTC().Format(time.RFC3339), created.Format(time.RFC3339), delta)
	}
}

// TestJobRow_JiraCSV_CycleTimeOfAnImportedIssueIsNotNegative is the half a column read cannot do.
// It is the whole reason `created_at` is worth a merge: the number the product SHOWS is wrong, not
// merely absent.
func TestJobRow_JiraCSV_CycleTimeOfAnImportedIssueIsNotNegative(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, _, _ := jiraCSVCreatedFixture()
	wsID := runJiraCSVCreatedImport(t, d, body)

	stats, err := analytics.New(d.Pool).GetTimeToResolution(ctx, wsID, "", 365)
	if err != nil {
		t.Fatalf("resolution stats: %v", err)
	}
	if stats.MedianHours <= 0 {
		t.Errorf("median time to resolution = %.1f hours for an issue Jira opened %d days ago and "+
			"finished %d days ago.\nA negative or zero cycle time is completed_at (past) minus "+
			"created_at (the import instant).",
			stats.MedianHours, jiraCSVCreatedDaysAgo, jiraCSVResolvedDaysAgo)
	}
	if delta := stats.MedianHours - jiraCSVTrueCycleHours; delta > 24 || delta < -24 {
		t.Errorf("median time to resolution = %.1f hours, want ≈ %.0f (Resolved − Created)",
			stats.MedianHours, jiraCSVTrueCycleHours)
	}
}
