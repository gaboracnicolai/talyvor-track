package importer_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// jira_csv_updated_job_test.go — the `Updated` column driven END TO END on real Postgres and then
// READ BACK THROUGH A SURFACE THAT CONSUMES IT.
//
// ⚠ THIS FILE EXISTS BECAUSE A STOP REASON WAS WRONG, NOT BECAUSE A FIELD WAS MISSING. #83 scoped
// `updated_at` out with "nothing in Track reads updated_at for a report", and #84 wrote that down
// again while flagging it as UNMEASURED. It is false, and it is false in four places — MEASURED at
// `d3aaaca` by enumerating every read of the column rather than by reading the importer:
//
//	frontend/src/components/issue/IssueRow.tsx:58   relativeTime(issue.updated_at)  — EVERY ROW
//	frontend/src/components/issue/IssueList.tsx:48  sorts the list by updated_at DESC
//	internal/issue/store.go:1135                    Search ORDER BY updated_at DESC
//	internal/issue/store.go:648                     updated_at is in the API's sort whitelist
//	internal/analytics/engine.go:416,433,483,508    the AI-cost report's window AND its x-axis
//	                                                (`date_trunc('day', updated_at) AS day`)
//
// The biggest consumer is not a report at all. It is the issue list — the product's main screen —
// which orders by recency and prints "updated <n> ago" on every row.
//
// ⚠ WHY A COLUMN ASSERTION IS NOT ENOUGH, inherited verbatim from #83's file and true again here:
// `issues.updated_at` is `TIMESTAMPTZ DEFAULT NOW()` and NEITHER write statement names it, so every
// imported row carries the instant the import ran — always non-null, always looking populated, the
// wrong value byte-indistinguishable in shape from the right one. The difference is only observable
// in what the product DOES with it, which is what the second test below asserts.
const (
	// HARDCODED, not read from the package constant — #75's C6: an assertion that formats with the
	// same constant the code parses with compares the constant to itself and passes for every value.
	jiraCSVUpdatedTestLayout = "2/Jan/2006 3:04 PM"

	// A backlog issue nobody has touched in 200 days. Inside the 365-day analytics window on
	// purpose, for the same reason #83's fixture is computed rather than written down: a hardcoded
	// date silently ages out of the window and the test stops testing anything while staying green.
	jiraCSVUpdatedDaysAgo = 200
)

// jiraCSVUpdatedFixture returns a one-row export whose issue was OPENED 300 days ago and LAST
// TOUCHED 200 days ago. Created is carried too — it is already landed, and leaving it out would let
// this fixture pass through a mapper that reads neither.
func jiraCSVUpdatedFixture() (csv string, created, updated time.Time) {
	now := time.Now().UTC()
	// Truncate to the minute: the layout carries no seconds, so a round trip loses them and an
	// equality assertion would fail for a reason that has nothing to do with the finding.
	created = now.Add(-300 * 24 * time.Hour).Truncate(time.Minute)
	updated = now.Add(-time.Duration(jiraCSVUpdatedDaysAgo) * 24 * time.Hour).Truncate(time.Minute)
	csv = "Summary,Description,Status,Priority,Created,Updated\n" +
		fmt.Sprintf("widget beta untouched for %d days,d,In Progress,High,%s,%s\n",
			jiraCSVUpdatedDaysAgo,
			created.Format(jiraCSVUpdatedTestLayout),
			updated.Format(jiraCSVUpdatedTestLayout))
	return csv, created, updated
}

func runJiraCSVUpdatedImport(t *testing.T, d *testutil.DB, body string) (wsID, teamID string) {
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
	return ws.ID, team.ID
}

// TestJobRow_JiraCSV_ImportedIssueKeepsTheDateJiraLastUpdatedIt is the column half.
func TestJobRow_JiraCSV_ImportedIssueKeepsTheDateJiraLastUpdatedIt(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, _, updated := jiraCSVUpdatedFixture()
	wsID, _ := runJiraCSVUpdatedImport(t, d, body)

	var got time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT updated_at FROM issues WHERE workspace_id = $1`, wsID).Scan(&got); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if delta := got.UTC().Sub(updated); delta > time.Minute || delta < -time.Minute {
		t.Errorf("updated_at = %s, want %s (the Updated column) — off by %s.\n"+
			"A defaulted updated_at is the import instant, so every imported issue reads as touched "+
			"just now and the list the product sorts by recency is ordered by import order.",
			got.UTC().Format(time.RFC3339), updated.Format(time.RFC3339), delta)
	}
}

// TestJobRow_JiraCSV_AStaleImportDoesNotOutrankTodaysWork is the half a column read cannot do, and
// is the reason this field is worth a merge: the ORDER the product SHOWS is wrong, not merely a
// timestamp that is absent.
//
// A native issue edited during the test must outrank a Jira issue untouched for 200 days in the
// query the product uses to list by recency (issue.Store.Search, ORDER BY updated_at DESC). The
// native issue is created FIRST and the import runs SECOND, so with a defaulted updated_at the
// imported row's timestamp is strictly LATER and the assertion fails deterministically — this is
// not a tie whose order happens to vary.
func TestJobRow_JiraCSV_AStaleImportDoesNotOutrankTodaysWork(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := issue.NewStore(d.Pool)

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// Today's work, created before the import so a defaulted updated_at can only beat it.
	// CreatorID is a HUMAN here on purpose, not model.ImporterCreatorID: this row stands for a
	// person's current work, and the upsert's import-ownership predicate must never reach it.
	native, err := store.Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID,
		Title:       "widget alpha edited today",
		Description: "current work",
		CreatorID:   "creator-native-w34-updated",
		Status:      model.StatusTodo, Priority: model.PriorityMedium,
	})
	if err != nil {
		t.Fatalf("create native issue: %v", err)
	}

	body, _, _ := jiraCSVUpdatedFixture()
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(store))
	if _, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	got, err := store.Search(ctx, ws.ID, "widget", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("search returned %d issues, want 2 (the native one and the imported one) — the "+
			"ordering assertion below is meaningless unless both rows are present", len(got))
	}
	if got[0].ID != native.ID {
		t.Errorf("most-recently-updated issue is %q, want %q.\n"+
			"A Jira issue nobody has touched in %d days outranks work edited today in the query the "+
			"product sorts the issue list by (issue/store.go:1135, ORDER BY updated_at DESC), and "+
			"IssueRow.tsx prints it as updated just now. That is a defaulted updated_at, not a fact "+
			"about the backlog.",
			got[0].Title, native.Title, jiraCSVUpdatedDaysAgo)
	}
}
