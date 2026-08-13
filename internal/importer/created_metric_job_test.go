package importer_test

// created_metric_job_test.go — the issues an import writes, counted in the metric whose Help says
// "Total number of issues created".
//
// ⚠⚠ THE COUNTER WAS INCREMENTED AT THE HTTP HANDLER, NOT AT THE WRITE. `metrics.IssuesCreated`
// had exactly ONE production call site — internal/issue/handler.go, the POST /v1/issues path — and
// the repository has FIVE production paths that create an issue: that handler, this importer's
// upsert branch and its Create branch (source.go), the MCP tool surface (mcp/server.go) and the
// automation engine (automation/engine.go). Four of the five moved no counter. MEASURED through
// the shipped async runner on real Postgres before the fix: a two-row jira_csv job reporting
// `succeeded imported=2`, TWO rows in the issues table, and `track_issues_created_total` for that
// workspace UNCHANGED AT ZERO.
//
// ⚠ AND NOTHING ANYWHERE ASSERTED IT: `metrics.` appeared in ZERO test files in the whole
// repository, so neither counter had ever been observed to move on ANY path. A metric that is
// wired once and asserted nowhere is not a measurement of the product — it is a measurement of one
// route, published under a name that claims the total.
//
// This file is the END-TO-END half (the operator's number, off the shipped runner). The
// per-door half is internal/issue/created_metric_realpg_test.go, at the layer the fix lives on.

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/metrics"
	tt "github.com/talyvor/track/internal/testutil"
)

// createdTotalFor sums track_issues_created_total over the (workspace, team, status) series this
// workspace can have produced. The statuses come from the ISSUES TABLE rather than from a guess
// about the mapping, so the sum cannot silently miss a series by predicting the wrong status —
// which would read as "the counter did not move" and confirm the finding for the wrong reason.
//
// ⚠ WithLabelValues MINTS a zero child when the series does not exist, which is exactly what a
// correct read of "this workspace created nothing" looks like. That is why the caller asserts a
// DELTA around the import rather than an absolute: the whole test binary shares one global
// counter, and other tests in this package import into their own workspaces.
func createdTotalFor(t *testing.T, d *tt.DB, wsID, teamID string) float64 {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT DISTINCT status FROM issues WHERE workspace_id=$1`, wsID)
	if err != nil {
		t.Fatalf("read statuses: %v", err)
	}
	defer rows.Close()
	statuses := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan status: %v", err)
		}
		statuses = append(statuses, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("statuses: %v", err)
	}
	var sum float64
	for _, s := range statuses {
		sum += testutil.ToFloat64(metrics.IssuesCreated.WithLabelValues(wsID, teamID, s))
	}
	return sum
}

// TestJobRow_JiraCSV_EveryIssueTheImportCreatesIsCountedInIssuesCreatedTotal is the operator-facing
// assertion: the number an import puts in the issues table and the number it puts in the counter
// that claims to total issue creation are the same number.
func TestJobRow_JiraCSV_EveryIssueTheImportCreatesIsCountedInIssuesCreatedTotal(t *testing.T) {
	d := tt.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	before := createdTotalFor(t, d, ws.ID, team.ID)

	job := importJiraCSVInto(t, d, ws.ID, team.ID, jobJiraCSVNoBOMExport)
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("PREMISE FAILED: job = %s imported=%d skipped=%d failed=%d %q, want succeeded/2 — "+
			"nothing below is readable", job.Status, job.Imported, job.Skipped, job.Failed, job.ErrorSummary)
	}
	if n := issuesInWorkspace(t, d, ws.ID); n != 2 {
		t.Fatalf("PREMISE FAILED: %d rows in issues, want 2 — the counter assertion below would be "+
			"comparing against an import that did not happen", n)
	}

	got := createdTotalFor(t, d, ws.ID, team.ID) - before
	if got != 2 {
		t.Errorf("track_issues_created_total moved by %v for this workspace, want 2.\n"+
			"TWO issues were written to the issues table by the shipped runner and the counter whose "+
			"Help reads \"Total number of issues created\" did not see them. The increment lives at "+
			"the POST /v1/issues handler, so every issue this product creates by IMPORT — the bulk "+
			"path, the one that writes thousands at a time — is invisible to it, as are the MCP tool "+
			"surface and the automation engine.", got)
	}
}
