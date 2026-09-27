package importer_test

// payload_lifetime_test.go — WHAT HAPPENS TO THE UPLOADED FILE AFTER THE IMPORT IS OVER (B18.30).
//
// A succeeded import's uploaded file is deleted the moment the runner records it succeeded. A failed
// or partial import's file is kept for importer.FailedPayloadRetention (7 days) so its owner can see
// what went wrong, then JobStore.PrunePayloads deletes it. The job row — status, counts, warnings — is
// kept either way. A deleted workspace's payloads go with it when internal/workspace/purge.go removes
// the workspace's import_jobs (import_job_payloads cascades from the job).

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// A small Jira export standing in for a real one: the payload is the customer's own text.
const retentionJiraCSV = "Summary,Issue key,Issue id,Issue Type,Status,Project key,Project name,Project type,Priority,Reporter,Created,Updated\n" +
	"Customer reported a billing discrepancy,ACME-1,10000,Bug,To Do,ACME,Acme,software,High,Dana Whitfield,6/21/2025 16:14,6/21/2025 17:13\n" +
	"Rotate the production signing key,ACME-2,10001,Task,To Do,ACME,Acme,software,Medium,Dana Whitfield,6/21/2025 16:15,6/21/2025 16:21\n"

func hasPayload(t *testing.T, d *testutil.DB, jobID string) bool {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM import_job_payloads WHERE job_id=$1`, jobID).Scan(&n); err != nil {
		t.Fatalf("count payload: %v", err)
	}
	return n > 0
}

// ---------------------------------------------------------------------------------------------
// (1) A succeeded import's upload is gone as soon as it succeeds, driven through the shipped runner.
// ---------------------------------------------------------------------------------------------

func TestPayload_DeletedWhenTheImportSucceeds(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	js := importer.NewJobStore(d.Pool)
	runner := importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(retentionJiraCSV))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if !hasPayload(t, d, jobID) {
		t.Fatal("PREMISE FAILED: a created job has no payload row")
	}
	if did, err := runner.RunOnce(ctx); err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}
	job, err := js.Get(ctx, jobID)
	if err != nil || job == nil {
		t.Fatalf("get job: job=%v err=%v", job, err)
	}
	if job.Status != importer.JobSucceeded || job.Imported != 2 {
		t.Fatalf("PREMISE FAILED: job = %s imported=%d, want succeeded/2", job.Status, job.Imported)
	}
	if hasPayload(t, d, jobID) {
		t.Error("the uploaded file of a succeeded import is still stored")
	}
}

// ---------------------------------------------------------------------------------------------
// (2) A failed import's upload is kept for 7 days, then pruned; the job row is kept.
// ---------------------------------------------------------------------------------------------

func TestPayload_FailedImportKeptSevenDaysThenPruned(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	js := importer.NewJobStore(d.Pool)

	jobID, err := js.Create(ctx, ws.ID, team.ID, "jira_csv", []byte(retentionJiraCSV))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := js.Finish(ctx, jobID, ws.ID, importer.JobFailed, 0, 0, 2, "boom", nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	var finished time.Time
	if err := d.Pool.QueryRow(ctx, `SELECT finished_at FROM import_jobs WHERE id=$1`, jobID).Scan(&finished); err != nil {
		t.Fatalf("read finished_at: %v", err)
	}

	if _, err := js.PrunePayloads(ctx, finished.Add(6*24*time.Hour)); err != nil {
		t.Fatalf("prune at 6 days: %v", err)
	}
	if !hasPayload(t, d, jobID) {
		t.Fatal("a failed import's upload was deleted after 6 days; it is kept for 7")
	}

	if _, err := js.PrunePayloads(ctx, finished.Add(importer.FailedPayloadRetention)); err != nil {
		t.Fatalf("prune at 7 days: %v", err)
	}
	if hasPayload(t, d, jobID) {
		t.Error("a failed import's upload is still stored 7 days after it failed")
	}
	if job, err := js.Get(ctx, jobID); err != nil || job == nil || job.Status != importer.JobFailed {
		t.Errorf("the job row must outlive its payload: job=%v err=%v", job, err)
	}
}

// ---------------------------------------------------------------------------------------------
// (3) The catalog census behind the workspace purge.
// ---------------------------------------------------------------------------------------------

// TestMeasured_TheWorkspaceChildTablesThatRefuseADelete reads the DELETE action of every foreign key
// pointing at workspaces OUT OF THE CATALOG, not out of the migration text — the migrations are 26
// files and a later ALTER would not show up in the CREATE TABLE that declared the column.
//
// Pinning the two sets by name is deliberate: a new workspace-scoped table inherits one of these
// policies silently, and this is the only place that makes the choice visible.
func TestMeasured_TheWorkspaceChildTablesThatRefuseADelete(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	rows, err := d.Pool.Query(ctx, `
		SELECT tc.relname, c.confdeltype
		FROM pg_constraint c
		JOIN pg_class tc ON tc.oid = c.conrelid
		JOIN pg_class rc ON rc.oid = c.confrelid
		WHERE c.contype = 'f' AND rc.relname = 'workspaces'`)
	if err != nil {
		t.Fatalf("read foreign keys: %v", err)
	}
	defer rows.Close()

	var restrict, cascade []string
	for rows.Next() {
		var table string
		var action byte
		if err := rows.Scan(&table, &action); err != nil {
			t.Fatalf("scan: %v", err)
		}
		switch action {
		case 'a', 'r': // NO ACTION / RESTRICT — both refuse
			restrict = append(restrict, table)
		case 'c':
			cascade = append(cascade, table)
		default:
			t.Errorf("%s: unexpected ON DELETE action %q — classify it here", table, string(action))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(restrict)
	sort.Strings(cascade)

	if len(restrict)+len(cascade) < 15 {
		t.Fatalf("REFUSING: only %d foreign keys to workspaces were found. The schema has more than "+
			"that, so this census is reading an unmigrated database and its lists mean nothing.",
			len(restrict)+len(cascade))
	}

	wantRestrict := []string{
		"ai_spend_events", "automation_rules", "cycles", "import_jobs", "issue_relations",
		"issues", "labels", "members", "milestones", "notifications", "projects", "teams",
		"time_entries", "workspace_integrations",
	}
	wantCascade := []string{
		"custom_fields", "feature_boards", "feature_posts", "guest_invites", "guests",
		"issue_scores", "issue_templates",
	}

	if strings.Join(restrict, ",") != strings.Join(wantRestrict, ",") {
		t.Errorf("the tables that REFUSE a workspace delete changed.\n got: %v\nwant: %v\n"+
			"Every table here refuses the final DELETE FROM workspaces unless the purge removes its rows "+
			"first: a new one must be added to purgeOrder in internal/workspace/purge.go, or deleted "+
			"workspaces holding its rows are never purged. If a table moved to CASCADE, say so.",
			restrict, wantCascade)
	}
	if strings.Join(cascade, ",") != strings.Join(wantCascade, ",") {
		t.Errorf("the tables that CASCADE on a workspace delete changed.\n got: %v\nwant: %v\n"+
			"A table added here is tenant data that a single owner-authenticated DELETE now removes.",
			cascade, wantCascade)
	}

	// import_jobs is named explicitly because it is this file's subject: a purged workspace's payloads
	// go by cascade from its jobs, which purgeOrder deletes before the workspace.
	found := false
	for _, r := range restrict {
		if r == "import_jobs" {
			found = true
		}
	}
	if !found {
		t.Errorf("import_jobs is no longer in the refusing set. If it now cascades, a workspace " +
			"delete reaches the uploaded payloads — which is a retention decision and must be " +
			"written down, in migration 0020 and here.")
	}
}
