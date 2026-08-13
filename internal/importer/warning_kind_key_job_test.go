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

// warning_kind_key_job_test.go — the free-text group key measured where it lands: the TEXT[] column
// and the JSON the status endpoint serves, driven through the async runner on real Postgres.
//
// ⚠ NOT THE UNIT TEST AGAIN. warning_kind_key_test.go proves renderWarnings groups wrongly and that
// the shipped jiraRowMapper feeds it the values that make it wrong. Neither of those touches
// import_jobs.warnings, which is where the array actually has a cardinality, an operator actually
// reads it, and migration 0026's NOT NULL actually applies. #80's own bound has a job-level twin
// (warning_bound_job_test.go) for exactly this reason; this is the case that twin's fixture cannot
// reach, because its CSV has no "Status Category" column.
//
// MEASURED BEFORE THE FIX, this harness, 400 rows: cardinality(warnings) = 402.

// jiraCSVWithFreeTextStatusCategory carries `Created` and `Updated` so the ONLY unbounded kind under
// test is the status/category one — their absence is its own note kind, and leaving either out would
// make this a test of how many KINDS the report holds rather than of the per-kind bound. Same
// reasoning, and same two columns, as csvWithDistinctBadDates.
func jiraCSVWithFreeTextStatusCategory(n int) string {
	var b strings.Builder
	b.WriteString("Issue key,Summary,Description,Status,Priority,Status Category,Created,Updated\n")
	for i := 0; i < n; i++ {
		// Status AND Status Category both distinct per row: that pair is one FieldNote, and before
		// the fix one FieldNote per row was one GROUP per row.
		fmt.Fprintf(&b, "ENG-%d,Issue %d,d,Statuz%d,High,Categoree%d,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n",
			i, i, i, i)
	}
	return b.String()
}

func TestJobRow_AFreeTextStatusCategoryDoesNotUnboundTheWarningsColumn(t *testing.T) {
	const rows = 400
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(jiraCSVWithFreeTextStatusCategory(rows)))
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
	// Every row still IMPORTS. A degraded field is not a failed row, and a bound that turned one
	// into the other would be a worse defect than the one under test.
	if j.Status != importer.JobSucceeded || j.Imported != rows || j.Failed != 0 {
		t.Fatalf("job = {status:%s imported:%d failed:%d}, want {succeeded %d 0}",
			j.Status, j.Imported, j.Failed, rows)
	}

	// THE ASSERTION NO RETURN VALUE CAN SATISFY: the array's real cardinality, read out of Postgres.
	var elems int
	if err := d.Pool.QueryRow(ctx,
		`SELECT cardinality(warnings) FROM import_jobs WHERE id=$1`, jobID).Scan(&elems); err != nil {
		t.Fatal(err)
	}
	// 11 = maxWarningExemplars + 1 summary, hardcoded for #75's C6 reason (a guard that reads the
	// constant it is checking compares it to itself and passes for every value). The status/category
	// kind is the only unbounded candidate here, so its whole budget is 11 and nothing else in this
	// export contributes: Created and Updated are present, Description is present, Labels is absent
	// (one line, and only on a row that UPDATED an existing issue — none do here).
	if elems > 11 {
		t.Errorf("warnings TEXT[] holds %d elements after a %d-row import, want at most 11 — "+
			"one uploaded CSV, one column, one JSON response", elems, rows)
	}
	if len(j.Warnings) != elems {
		t.Errorf("the JSON the status endpoint serves has %d lines but the column has %d", len(j.Warnings), elems)
	}

	// AND NOTHING IS HIDDEN. A bound that passes by deleting the finding is not a bound: the report
	// must still say a category could not be placed, and must still count what it did not list.
	joined := strings.Join(j.Warnings, "\n")
	if !strings.Contains(joined, "statusCategory") {
		t.Errorf("the report no longer names the category it could not place:\n%s", joined)
	}
	if !strings.Contains(joined, fmt.Sprintf("%d further distinct", rows-10)) {
		t.Errorf("the summary does not name the %d findings it did not list:\n%s", rows-10, joined)
	}
	if !strings.Contains(joined, fmt.Sprintf("across %d issue(s)", rows-10)) {
		t.Errorf("the summary does not name the issues those findings covered:\n%s", joined)
	}
}

// THE FLOOR, IN THE OTHER DIRECTION — a real Jira export's category vocabulary is FOUR values, and
// the fix must not collapse a report that was already correct. Two categories that resolve to two
// different Track statuses stay two findings with their own counts, because Mapped is still in the
// group key and the category is what decides it.
func TestJobRow_ResolvableCategoriesStillReportSeparatelyWithTheirCounts(t *testing.T) {
	const per = 5
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	var b strings.Builder
	b.WriteString("Issue key,Summary,Description,Status,Priority,Status Category,Created,Updated\n")
	for i := 0; i < per; i++ {
		// One unrecognised status name, two different categories that DO resolve — to two
		// different Track statuses.
		fmt.Fprintf(&b, "ENG-a%d,Issue a%d,d,Bespoke,High,To Do,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n", i, i)
		fmt.Fprintf(&b, "ENG-b%d,Issue b%d,d,Bespoke,High,In Progress,23/Jul/2026 7:36 PM,23/Jul/2026 7:36 PM\n", i, i)
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
	joined := strings.Join(j.Warnings, "\n")
	if len(j.Warnings) != 2 {
		t.Fatalf("warnings = %d lines, want exactly 2 (one per resolved category):\n%s", len(j.Warnings), joined)
	}
	for _, want := range []string{
		`resolved via statusCategory "To Do" as "todo"`,
		`resolved via statusCategory "In Progress" as "in_progress"`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the report lost %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, fmt.Sprintf("on %d issue(s)", per)) != 2 {
		t.Errorf("both categories should carry their own count of %d issues:\n%s", per, joined)
	}
	if strings.Contains(joined, "further distinct") {
		t.Errorf("a summary line appeared for a report under the bound:\n%s", joined)
	}
}
