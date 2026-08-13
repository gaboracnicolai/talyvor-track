package importer_test

// updated_metric_job_test.go — the issues a RE-import overwrites, counted in the metric whose Help
// says "Total number of issue updates".
//
// ⚠⚠ THE OPERATOR-FACING HALF OF THE SAME DEFECT created_metric_job_test.go pinned one counter ago.
// `metrics.IssuesUpdated` had exactly ONE production call site — internal/issue/handler.go's PATCH
// /v1/issues/{id} — while fifteen production paths update an issue. This file drives the one that
// writes the most rows per call: a re-import of a keyed export, where every row takes
// UpsertByIdentifier's conflict arm and overwrites an issue that already existed.
//
// ⚠ THE FIXTURE IS THE SAME TWO-ROW EXPORT IMPORTED TWICE, WHICH IS THE POINT. The first job is the
// control: it INSERTS, so it must move the updated counter by ZERO. The second job touches the same
// two identifiers and writes no new rows — issues stays at 2 — so every unit of the counter it
// moves is an overwrite the operator actually paid for.

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/metrics"
	tt "github.com/talyvor/track/internal/testutil"
)

// updatedTotalFor sums track_issues_updated_total over the (workspace, team, status) series this
// workspace can have produced. The statuses come from the ISSUES TABLE rather than from a guess
// about the Jira status mapping, so the sum cannot silently miss a series by predicting the wrong
// status — which would read as "the counter did not move" and confirm the finding for the wrong
// reason. Same instrument, same reasoning, as createdTotalFor above it.
func updatedTotalFor(t *testing.T, d *tt.DB, wsID, teamID string) float64 {
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
		sum += testutil.ToFloat64(metrics.IssuesUpdated.WithLabelValues(wsID, teamID, s))
	}
	return sum
}

func TestJobRow_JiraCSV_EveryIssueAReImportOverwritesIsCountedInIssuesUpdatedTotal(t *testing.T) {
	d := tt.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// FIRST IMPORT — the control. Two INSERTs, zero updates.
	beforeFirst := updatedTotalFor(t, d, ws.ID, team.ID)
	first := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVKeyedExport)
	if first.Status != importer.JobSucceeded || first.Imported != 2 {
		t.Fatalf("PREMISE FAILED: first job = %s imported=%d skipped=%d failed=%d %q, want succeeded/2 — "+
			"nothing below is readable", first.Status, first.Imported, first.Skipped, first.Failed,
			first.ErrorSummary)
	}
	if n := issuesInWorkspace(t, d, ws.ID); n != 2 {
		t.Fatalf("PREMISE FAILED: %d rows in issues after the first import, want 2", n)
	}
	if got := updatedTotalFor(t, d, ws.ID, team.ID) - beforeFirst; got != 0 {
		t.Errorf("track_issues_updated_total moved by %v on an import that INSERTED both rows, want 0 "+
			"— a first import overwrites nothing, and counting it would report every import twice", got)
	}

	// SECOND IMPORT — the same two keys. Every row takes the conflict arm.
	beforeSecond := updatedTotalFor(t, d, ws.ID, team.ID)
	second := importJiraCSVInto(t, d, ws.ID, team.ID, jiraCSVKeyedExport)
	if second.Status != importer.JobSucceeded || second.Imported != 2 {
		t.Fatalf("PREMISE FAILED: second job = %s imported=%d skipped=%d failed=%d %q, want "+
			"succeeded/2 — the re-import did not take the branch this test exists to measure",
			second.Status, second.Imported, second.Skipped, second.Failed, second.ErrorSummary)
	}
	if n := issuesInWorkspace(t, d, ws.ID); n != 2 {
		t.Fatalf("PREMISE FAILED: %d rows in issues after the re-import, want 2 — the second job "+
			"CREATED rows, so it is not the update path", n)
	}

	if got := updatedTotalFor(t, d, ws.ID, team.ID) - beforeSecond; got != 2 {
		t.Errorf("track_issues_updated_total moved by %v for this workspace, want 2.\n"+
			"The shipped runner reported `succeeded imported=2` against a table that gained NO rows — "+
			"two existing issues were overwritten — and the counter whose Help reads \"Total number of "+
			"issue updates\" did not see them. The increment lives at the PATCH /v1/issues/{id} "+
			"handler, so every issue this product updates by IMPORT, by MCP tool call, by automation "+
			"rule and by kanban drag is invisible to it.", got)
	}
}
