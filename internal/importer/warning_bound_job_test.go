package importer_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// warning_bound_job_test.go — the bound measured where it matters: the TEXT[] column and the JSON the
// status endpoint serves, driven through the async runner on real Postgres.
//
// ⚠ NOT THE UNIT TEST TWICE. renderWarnings is a pure function over a map; this asserts what actually
// LANDS. The async path is the one a real import takes (the inline route dies on the 30s timeout),
// migration 0026 exists precisely because a fix stopping at ImportResult would be inert there, and
// `warnings` is NOT NULL — so "bounded" has to be true of the column, not of a return value.
//
// MEASURED BEFORE THIS MERGE, same harness: 3,000 rows with per-row-distinct unparseable Due Dates
// produced 3,000 array elements; 20,000 rows produced 20,000. One upload, one row, one response.

func csvWithDistinctBadDates(n int) string {
	var b strings.Builder
	// `Created` and `Updated` are present so the ONLY unbounded note kind under test is the Due
	// Date one. Both are date columns whose absence is its own note kind; leaving either out would
	// make this a test of how many KINDS the report holds rather than of the per-kind bound.
	b.WriteString("Summary,Description,Status,Priority,Due Date,Created,Updated\n")
	for i := 0; i < n; i++ {
		// Unparseable by every pinned layout AND distinct per row. The first version of this
		// generator used (i%28, i%24, i%60, i%60), whose period is 840 — the measurement saturated
		// on the generator rather than on the product, and reported a bound that did not exist.
		fmt.Fprintf(&b, "Issue %d,d,To Do,High,2025-01-01T00:00:00.%09dZ,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n", i, i)
	}
	return b.String()
}

func TestJobRow_WarningsAreBoundedInPostgres(t *testing.T) {
	const rows = 1200
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(csvWithDistinctBadDates(rows)))
	if err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	j, err := js.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	// Every row still IMPORTS — a degraded field is not a failed row, the distinction #72 built the
	// column for. The bound must not have turned a warning into a rejection.
	if j.Status != importer.JobSucceeded || j.Imported != rows || j.Failed != 0 {
		t.Fatalf("job = {status:%s imported:%d failed:%d}, want {succeeded %d 0}",
			j.Status, j.Imported, j.Failed, rows)
	}

	// THE ASSERTION THE MAPPER CANNOT SATISFY: read the array's real cardinality out of Postgres.
	var elems int
	if err := d.Pool.QueryRow(ctx,
		`SELECT cardinality(warnings) FROM import_jobs WHERE id=$1`, jobID).Scan(&elems); err != nil {
		t.Fatal(err)
	}
	if elems > 11 { // maxWarningExemplars + 1, hardcoded: a guard that reads the constant it is
		// checking compares it to itself and passes for every value (#75's C6).
		t.Errorf("warnings TEXT[] holds %d elements after a %d-row import, want at most 11", elems, rows)
	}
	if len(j.Warnings) != elems {
		t.Errorf("the JSON the status endpoint serves has %d lines but the column has %d", len(j.Warnings), elems)
	}

	// AND NOTHING IS HIDDEN — the report must still say how much it could not place.
	joined := strings.Join(j.Warnings, "\n")
	if !strings.Contains(joined, fmt.Sprintf("%d further distinct", rows-10)) {
		t.Errorf("the summary does not name the %d values it did not list:\n%s", rows-10, joined)
	}
	if !strings.Contains(joined, fmt.Sprintf("across %d issue(s)", rows-10)) {
		t.Errorf("the summary does not name the issues those values covered:\n%s", joined)
	}
}

// The other direction, so the bound cannot pass by emptying the report: an import whose degraded
// values REPEAT still produces its one line with its full count, however many rows carry it.
func TestJobRow_ARepeatedValueStillReportsItsFullCount(t *testing.T) {
	const rows = 1200
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	var b strings.Builder
	// Same reason as the bound fixture above: `Created` and `Updated` present so the status note
	// is the only kind this test can see.
	b.WriteString("Summary,Description,Status,Priority,Created,Updated\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "Issue %d,d,Deployed,High,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n", i)
	}
	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	j, err := js.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Warnings) != 1 {
		t.Fatalf("warnings = %d lines, want exactly 1:\n%s", len(j.Warnings), strings.Join(j.Warnings, "\n"))
	}
	if !strings.Contains(j.Warnings[0], fmt.Sprintf("on %d issue(s)", rows)) {
		t.Errorf("the count was lost by the bound: %q", j.Warnings[0])
	}
	if strings.Contains(j.Warnings[0], "further distinct") {
		t.Errorf("a summary line appeared for a single distinct value: %q", j.Warnings[0])
	}
}
