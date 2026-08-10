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

// linear_csv_updated_job_test.go — the `Updated` column of a Linear CSV export, driven END TO END
// through the async runner on real Postgres and then READ BACK THROUGH THE SURFACE THAT CONSUMES
// IT.
//
// ⚠⚠ THIS IS THE FOURTH TRANSPORT AND THE OTHER THREE HAVE READ THIS COLUMN SINCE #85/#86.
// jiraRowMapper sets UpdatedAt (jira_csv_updated.go), and both API halves set it (api_updated.go).
// linearRowMapper does not — it is the only one of the four that leaves the column defaulted. THE
// ASYMMETRY IS WHAT MAKES THIS A DEFECT RATHER THAN AN UNDECIDED CONTRACT: the same fact, in the
// same product, arriving through two transports of the same shape, kept by one and dropped by the
// other. #97's argument, one package over.
//
// ⚠ #89's OWN NOTE ENUMERATED THE COLUMNS THIS TRANSPORT IGNORES AND `Updated` IS NOT IN THE LIST.
// It named exactly `ID` and `Estimate` (plus `Assignee`, deferred), and the queue then recorded
// "Created/Completed ARE NOW DONE ON ALL FOUR TRANSPORTS" with the remaining-fields list carrying
// no mention of Updated at all. The enumeration was taken from THIS PACKAGE'S FIXTURES, whose
// Linear header is the nine columns Linear's import documentation names —
// Title · Description · Priority · Status · Assignee · Created · Completed · Labels · Estimate —
// and that header HAS NO `Updated`. So the census could not see the column: the instrument was the
// fixture, and the fixture is not the export.
//
// ⚠ MEASURED AGAINST REAL EXPORT BYTES rather than against the fixture, by
// scripts/w34-linear-csv-updated-probe.py — 45 real Linear CSV exports that unrelated tenants
// committed to public repositories, the corpus #99 opened, three negative controls run first:
//
//	`Updated` in header       44 of 45 files, across ALL SIX header shapes (29/30/34 columns)
//	non-empty `Updated` cells 2,947 of 3,026 data rows (97.4%)
//	owners emitting it        every owner in the corpus
//
// The provenance is second-hand bytes and is NOT dressed up as equal to the Jira probe's first-hand
// ones (#75's overclaim). What makes it evidence is AGREEMENT ACROSS TENANTS THAT HAVE NEVER MET,
// six header shapes wide. And the fail-safe is the same one #99 relied on: buildIndex matches the
// FULL header, so an export that does not carry the column yields EXACTLY today's behaviour.
//
// ⚠ WHY A COLUMN ASSERTION CANNOT SEE IT, inherited from #83/#85 and true a fourth time:
// `issues.updated_at` is `TIMESTAMPTZ DEFAULT NOW()`, so an unsupplied value is not a null anybody
// can spot — it is a plausible timestamp with exactly the shape of a correct one. The difference is
// only observable in what the product DOES with it, which is what the second test asserts.
const (
	// HARDCODED, and hardcoded as the shape the probe MEASURED (2,195 of 2,947 real cells,
	// `2026-01-15T10:23:45.123Z`) rather than read from linearCSVTimeLayouts. A fixture that
	// formats with the same constant the code parses with compares the constant to itself and
	// passes for every possible value. The trailing Z here is literal layout text, not Go's
	// zone form, which is what makes this string genuinely independent of time.RFC3339.
	linearCSVUpdatedTestLayout = "2006-01-02T15:04:05.000Z"

	// A backlog issue nobody has touched in 200 days. Computed rather than written down for the
	// reason #83's fixture is: a hardcoded date ages out of the analytics window and the test
	// stops testing anything while staying green.
	linearCSVUpdatedDaysAgo = 200
)

// linearCSVUpdatedFixture returns a one-row export whose issue was OPENED 300 days ago and LAST
// TOUCHED 200 days ago.
//
// ⚠ THE HEADER IS THE ONE THE PROBE MEASURED, NOT THE ONE THIS PACKAGE'S OTHER FIXTURES CARRY.
// The nine-column documentation header the rest of the Linear tests use has no `Updated` in it,
// which is precisely how the column went uncounted for thirty-one merges — a fixture that cannot
// carry the value cannot fail when the value is dropped. `ID` is carried too, so the row travels
// #99's upsert route (the one a real export takes) rather than the keyless Create route.
func linearCSVUpdatedFixture() (csv string, created, updated time.Time) {
	now := time.Now().UTC()
	// Truncate to the millisecond: the layout carries three fractional digits, so a round trip
	// loses anything finer and an equality assertion would fail for a reason that has nothing to
	// do with the finding.
	created = now.Add(-300 * 24 * time.Hour).Truncate(time.Millisecond)
	updated = now.Add(-time.Duration(linearCSVUpdatedDaysAgo) * 24 * time.Hour).Truncate(time.Millisecond)
	csv = "ID,Team,Title,Description,Status,Priority,Labels,Created,Updated,Completed\n" +
		fmt.Sprintf("LIN-1,Eng,widget beta untouched for %d days,d,In Progress,High,,%s,%s,\n",
			linearCSVUpdatedDaysAgo,
			created.Format(linearCSVUpdatedTestLayout),
			updated.Format(linearCSVUpdatedTestLayout))
	return csv, created, updated
}

func runLinearCSVUpdatedImport(t *testing.T, d *testutil.DB, body string) (wsID, teamID string) {
	t.Helper()
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
	if _, err := js.Create(ctx, ws.ID, team.ID, "linear_csv", []byte(body)); err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	return ws.ID, team.ID
}

// TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearLastUpdatedIt is the column half.
func TestJobRow_LinearCSV_ImportedIssueKeepsTheDateLinearLastUpdatedIt(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	body, _, updated := linearCSVUpdatedFixture()
	wsID, _ := runLinearCSVUpdatedImport(t, d, body)

	var got time.Time
	if err := d.Pool.QueryRow(ctx,
		`SELECT updated_at FROM issues WHERE workspace_id = $1`, wsID).Scan(&got); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if delta := got.UTC().Sub(updated); delta > time.Minute || delta < -time.Minute {
		t.Errorf("updated_at = %s, want %s (the Updated column) — off by %s.\n"+
			"A defaulted updated_at is the import instant, so every issue a Linear CSV import "+
			"brings in reads as touched just now. jira_csv, jira_api and linear_api have all kept "+
			"this column since #85/#86; linear_csv is the only transport that drops it.",
			got.UTC().Format(time.RFC3339), updated.Format(time.RFC3339), delta)
	}
}

// TestJobRow_LinearCSV_AStaleImportDoesNotOutrankTodaysWork is the half a column read cannot do,
// and is the reason this field is worth a merge: the ORDER THE PRODUCT SHOWS is wrong, not merely a
// timestamp that is absent.
//
// A native issue created during the test must outrank a Linear issue untouched for 200 days in the
// query the product lists by (issue.Store.Search, ORDER BY updated_at DESC — and IssueList.tsx
// sorts the same way client-side while IssueRow.tsx prints "updated <n> ago" on every row). The
// native issue is written FIRST and the import runs SECOND, so with a defaulted updated_at the
// imported row's timestamp is strictly LATER and this fails deterministically — it is not a tie
// whose order happens to vary.
func TestJobRow_LinearCSV_AStaleImportDoesNotOutrankTodaysWork(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := issue.NewStore(d.Pool)

	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// Today's work, written before the import so a defaulted updated_at can only beat it.
	// CreatorID is a HUMAN on purpose, not model.ImporterCreatorID: this row stands for a person's
	// current work and the upsert's import-ownership predicate must never reach it.
	native, err := store.Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID,
		Title:       "widget alpha edited today",
		Description: "current work",
		CreatorID:   "creator-native-w34-linear-updated",
		Status:      model.StatusTodo, Priority: model.PriorityMedium,
	})
	if err != nil {
		t.Fatalf("create native issue: %v", err)
	}

	body, _, _ := linearCSVUpdatedFixture()
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(store))
	if _, err := js.Create(ctx, ws.ID, team.ID, "linear_csv", []byte(body)); err != nil {
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
			"A Linear issue nobody has touched in %d days outranks work created today in the query "+
			"the product sorts the issue list by (issue/store.go, ORDER BY updated_at DESC), and "+
			"IssueRow.tsx prints it as updated just now. That is a defaulted updated_at, not a fact "+
			"about the backlog.",
			got[0].Title, native.Title, linearCSVUpdatedDaysAgo)
	}
}
