package importer

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/integrations"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// ── T8 Build C.3 — end-to-end dispatch through the runner (real PG). ──

func testIntegrationStore(t *testing.T, d *testutil.DB) *integrations.Store {
	t.Helper()
	c, err := integrations.NewCipher(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	return integrations.NewStore(d.Pool, c)
}

// insertAPIJob inserts a pending *_api job directly — no payload row (the enqueue handler for *_api is a
// follow-up; C.3 delivers the runner dispatch). Returns the job id.
func insertAPIJob(t *testing.T, d *testutil.DB, wsID, teamID, sourceType string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO import_jobs (workspace_id, team_id, source_type, status)
		 VALUES ($1, $2, $3, 'pending') RETURNING id`, wsID, teamID, sourceType).Scan(&id); err != nil {
		t.Fatalf("insert api job: %v", err)
	}
	return id
}

// (g) END-TO-END DISPATCH: a linear_api job → runner loads the workspace's C.1 integration (GetDecrypted) →
// linearSource fetches (canned) → UpsertByIdentifier lands issues with provider-key identifiers in the job's
// workspace. Workspace comes ONLY from the job row (Build-B re-enforcement).
func TestRunner_LinearAPI_EndToEnd(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	srv := httptest.NewServer(cannedPages([]string{
		linPage(false, "", linNode("ENG-1", "Todo", 1), linNode("ENG-2", "Done", 2)),
	}, linPage(false, "")))
	defer srv.Close()

	istore := testIntegrationStore(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "linear", "api-token", "LINEAR-TEAM-KEY", srv.URL); err != nil {
		t.Fatal(err)
	}
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "linear_api")

	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).WithProviderConfig(istore)
	did, err := runner.RunOnce(ctx)
	if err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	var n int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM issues WHERE workspace_id=$1 AND identifier IN ('ENG-1','ENG-2')`, ws.ID).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("got %d issues with provider-key identifiers in the job's workspace, want 2", n)
	}
	job, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobSucceeded || job.Imported != 2 {
		t.Fatalf("job status=%s imported=%d, want succeeded/2", job.Status, job.Imported)
	}
}

// (h) NO-INTEGRATION: a linear_api job for a workspace with no integration → job fails cleanly (clear error),
// no panic. And with the credential store entirely absent (configs nil), likewise.
func TestRunner_LinearAPI_NoIntegration_FailsCleanly(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	// store present, but NO integration seeded for this workspace.
	istore := testIntegrationStore(t, d)
	jobID := insertAPIJob(t, d, ws.ID, team.ID, "linear_api")
	runner := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool))).WithProviderConfig(istore)
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce should not itself error (the job is marked failed): %v", err)
	}
	job, err := NewJobStore(d.Pool).Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobFailed {
		t.Fatalf("no-integration job status=%s, want failed", job.Status)
	}
	if !strings.Contains(job.ErrorSummary, "integration") && !strings.Contains(job.ErrorSummary, "configured") {
		t.Fatalf("error_summary should explain the missing integration: %q", job.ErrorSummary)
	}

	// credential store entirely absent (configs nil) → also fails cleanly, no panic.
	jobID2 := insertAPIJob(t, d, ws.ID, team.ID, "jira_api")
	runnerNoCfg := NewRunner(NewJobStore(d.Pool), New(issue.NewStore(d.Pool)))
	if _, err := runnerNoCfg.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce (no config) should not error: %v", err)
	}
	job2, err := NewJobStore(d.Pool).Get(ctx, jobID2)
	if err != nil {
		t.Fatal(err)
	}
	if job2.Status != JobFailed {
		t.Fatalf("configs-nil job status=%s, want failed", job2.Status)
	}
}
