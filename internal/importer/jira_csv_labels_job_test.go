package importer_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// jira_csv_labels_job_test.go — the repeated `Labels` columns driven END TO END on real Postgres,
// through the async runner and a jira_csv job, and read back OUT OF THE issues TABLE.
//
// ⚠ THIS IS NOT THE UNIT TEST TWICE, AND THIS PACKAGE HAS PAID FOR THAT TWICE ALREADY. #74 found the
// importer's UPSERT silently omitting `completed_at`, so a perfectly mapped value was discarded by
// the SQL; #78 found the SECOND COPY of that seam in issue.Store.Create, which is the statement every
// CSV row takes. A mapper fix that never reached the column would leave every assertion in
// jira_csv_labels_test.go green. `labels` is written as a TEXT[], which is a third shape again — the
// two date columns are scalars — so "it lands" is measured here rather than inherited.
//
// The header below is the measured export's shape: THREE columns spelled "Labels", because the
// widest issue in the result set has three, and every narrower row padded out with empties. See
// jira_csv_labels_test.go for the measurement and its negative controls.
// The `Created` column carries no labels meaning and is here because a real csv-all-fields export
// always has one: without it every row is legitimately warned about (jira_csv_created.go), and the
// "want none" assertion below would have had to be weakened to accommodate an unrelated merge.
const jiraCSVWithRepeatedLabels = "Summary,Description,Status,Priority,Labels,Labels,Labels,Due Date,Created\n" +
	"Widest work,d,Closed,High,alpha,beta,gamma,,23/Jul/2026 7:36 PM\n" +
	"Narrow work,d,To Do,High,alpha,,,,23/Jul/2026 7:36 PM\n" +
	"Middle work,d,To Do,High,alpha,beta,,,23/Jul/2026 7:36 PM\n" +
	"Unlabelled work,d,To Do,High,,,,,23/Jul/2026 7:36 PM\n"

func readIssueLabelsByTitle(t *testing.T, d *testutil.DB, wsID string) map[string][]string {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT title, labels FROM issues WHERE workspace_id = $1`, wsID)
	if err != nil {
		t.Fatalf("read back issues: %v", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var title string
		var labels []string
		if err := rows.Scan(&title, &labels); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[title] = labels
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

func TestJobRow_JiraCSV_EveryRepeatedLabelColumnLandsInPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(jiraCSVWithRepeatedLabels))
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
	// Every row lands and nothing is degraded — a label the mapper CAN place is not a warning.
	if j.Status != importer.JobSucceeded || j.Imported != 4 || j.Failed != 0 {
		t.Fatalf("job = {status:%s imported:%d failed:%d}, want {succeeded 4 0}", j.Status, j.Imported, j.Failed)
	}
	if len(j.Warnings) != 0 {
		t.Errorf("warnings = %v, want none — every label was placeable", j.Warnings)
	}

	got := readIssueLabelsByTitle(t, d, ws.ID)
	if len(got) != 4 {
		t.Fatalf("rows in Postgres = %d, want 4", len(got))
	}
	for _, tc := range []struct {
		title string
		want  []string
	}{
		{"Widest work", []string{"alpha", "beta", "gamma"}},
		// ⚠ THE ROW THE DEFECT ATE. Its labels sit in the FIRST columns and the last is the padding,
		// so before this merge it read the empty one and imported nothing at all. Five of the six
		// issues in the measured export are this shape.
		{"Narrow work", []string{"alpha"}},
		{"Middle work", []string{"alpha", "beta"}},
		{"Unlabelled work", []string{}},
	} {
		g := got[tc.title]
		if len(g) != len(tc.want) {
			t.Errorf("%s: labels in Postgres = %v (%d), want %v (%d)", tc.title, g, len(g), tc.want, len(tc.want))
			continue
		}
		for i := range tc.want {
			if g[i] != tc.want[i] {
				t.Errorf("%s: labels[%d] = %q, want %q (full: %v)", tc.title, i, g[i], tc.want[i], g)
			}
		}
	}
}

// The other direction, so the column cannot pass by always being written: an export with ONE Labels
// column still round-trips its comma-joined cell, and an export with NO Labels column writes an
// empty array rather than a NULL the read path would have to special-case.
func TestJobRow_JiraCSV_SingleAndAbsentLabelColumnsAreUnchanged(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	const csv = "Summary,Description,Status,Priority,Labels\n" +
		"Joined work,d,To Do,High,\"alpha, beta\"\n" +
		"Plain work,d,To Do,High,\n"
	const csvNoLabelColumn = "Summary,Description,Status,Priority\nNo column work,d,To Do,High\n"

	for _, payload := range []string{csv, csvNoLabelColumn} {
		if _, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(payload)); err != nil {
			t.Fatal(err)
		}
		if did, err := runner.RunOnce(ctx); err != nil || !did {
			t.Fatalf("RunOnce did=%v err=%v", did, err)
		}
	}

	got := readIssueLabelsByTitle(t, d, ws.ID)
	if len(got) != 3 {
		t.Fatalf("rows in Postgres = %d, want 3", len(got))
	}
	if g := got["Joined work"]; len(g) != 2 || g[0] != "alpha" || g[1] != "beta" {
		t.Errorf("Joined work: labels = %v, want [alpha beta] — the single comma-joined cell must be unchanged", g)
	}
	for _, title := range []string{"Plain work", "No column work"} {
		if g := got[title]; len(g) != 0 {
			t.Errorf("%s: labels = %v, want empty", title, g)
		}
	}
}

// A FLOOR THE OTHER TESTS CANNOT PROVIDE — this file must actually reach Postgres. testutil.New
// SKIPS cleanly without TRACK_TEST_DATABASE_URL, and a skipped file is a green file: the whole
// "a zero from an instrument that read nothing" class. This records, in the test output, that the
// rows really were written and read back through the real schema.
func TestJobRow_JiraCSV_LabelsHarnessReallyReachesPostgres(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	if _, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(jiraCSVWithRepeatedLabels)); err != nil {
		t.Fatal(err)
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	var n int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE workspace_id = $1 AND cardinality(labels) > 0`, ws.ID).Scan(&n); err != nil {
		t.Fatalf("count labelled issues: %v", err)
	}
	if n != 3 {
		t.Fatalf("issues with a non-empty labels array = %d, want 3 — the harness is not writing what it claims", n)
	}
	all := readIssueLabelsByTitle(t, d, ws.ID)
	var flat []string
	for _, v := range all {
		flat = append(flat, v...)
	}
	sort.Strings(flat)
	if got, want := strings.Join(flat, ","), "alpha,alpha,alpha,beta,beta,gamma"; got != want {
		t.Fatalf("every label value in Postgres = %q, want %q", got, want)
	}
}
