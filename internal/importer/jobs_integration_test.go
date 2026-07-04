package importer_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// ── T8 Build B — async import-job spine proofs. Reuses authz_test.go's seedMember/issueCount/secret. ──

const asyncLinearCSV = "Title,Description,Status,Priority,Labels\n" +
	"Async One,d,Todo,Urgent,bug\n" +
	"Async Two,d,Done,High,ui\n"

func newSpine(d *testutil.DB) (*importer.JobStore, *importer.Runner) {
	js := importer.NewJobStore(d.Pool)
	return js, importer.NewRunner(js, importer.New(issue.NewStore(d.Pool)))
}

func jobStatus(t *testing.T, d *testutil.DB, id string) *importer.Job {
	t.Helper()
	j, err := importer.NewJobStore(d.Pool).Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return j
}

// (a) LOAD-BEARING TENANCY: a job authorized for workspace A writes issues ONLY into A. Every avenue a
// workspace could sneak in is closed — the CSV even carries a workspace_id column set to B, and it is
// IGNORED (the mapper reads only known fields; the runner reads the workspace from the JOB ROW = A).
func TestRunner_WritesOnlyIntoJobWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	jobs, runner := newSpine(d)

	// A malicious CSV: an extra workspace_id column pointing at B. The mapper never reads it.
	malicious := "Title,Description,Status,Priority,Labels,workspace_id\n" +
		"Sneaky,d,Todo,High,bug," + wsB.ID + "\n"
	jobID, err := jobs.Create(ctx, wsA.ID, teamA.ID, "linear_csv", []byte(malicious))
	if err != nil {
		t.Fatal(err)
	}

	did, err := runner.RunOnce(ctx)
	if err != nil || !did {
		t.Fatalf("RunOnce did=%v err=%v", did, err)
	}

	if n := issueCount(t, d, wsA.ID); n != 1 {
		t.Fatalf("job for A must import into A: got %d issues in A, want 1", n)
	}
	if n := issueCount(t, d, wsB.ID); n != 0 {
		t.Fatalf("TENANCY BREACH: %d issues landed in B — the runner read a workspace from somewhere other than the job row", n)
	}
	if j := jobStatus(t, d, jobID); j.Status != importer.JobSucceeded || j.WorkspaceID != wsA.ID {
		t.Fatalf("job: status=%s workspace=%s, want succeeded/%s", j.Status, j.WorkspaceID, wsA.ID)
	}
}

// jobChain mounts the async JobHandler behind the REAL T9+T10 middleware (same stack as importChain).
func jobChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	h := importer.NewJobHandler(importer.NewJobStore(d.Pool))
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		h.Mount(r)
	})
	return r
}

func statusReq(id, email string) *http.Request {
	req := httptest.NewRequest("GET", "/v1/import/jobs/"+id, nil)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req
}

// (b) STATUS TENANCY: a member of the job's workspace can read it; a non-member is DENIED (403), never shown
// the data.
func TestJobStatus_TenancyScoped(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	seedMember(t, d, wsA.ID, "a@corp.com") // member of A only
	seedMember(t, d, wsB.ID, "b@corp.com") // member of B

	// A job in workspace B.
	jobID, err := importer.NewJobStore(d.Pool).Create(ctx, wsB.ID, teamB.ID, "linear_csv", []byte(asyncLinearCSV))
	if err != nil {
		t.Fatal(err)
	}
	h := jobChain(d)

	// member-of-A reading B's job → 403 (not the data).
	rrDeny := httptest.NewRecorder()
	h.ServeHTTP(rrDeny, statusReq(jobID, "a@corp.com"))
	if rrDeny.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant status read = %d, want 403; body=%s", rrDeny.Code, rrDeny.Body.String())
	}

	// member-of-B reading B's job → 200.
	rrOK := httptest.NewRecorder()
	h.ServeHTTP(rrOK, statusReq(jobID, "b@corp.com"))
	if rrOK.Code != http.StatusOK {
		t.Fatalf("member status read = %d, want 200; body=%s", rrOK.Code, rrOK.Body.String())
	}
}

// (c) PARTIAL OBSERVABILITY: a CSV with good rows + a malformed row ends status=partial with accurate
// imported/failed counts — durable + readable, the state a 30s-kill would lose.
func TestRunner_PartialImport_Observable(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	jobs, runner := newSpine(d)

	// 2 good rows + 1 raggedly-short row (fails per-row, not the batch).
	mixed := "Title,Description,Status,Priority,Labels\n" +
		"Good A,d,Todo,High,bug\n" +
		"bad-short-row\n" +
		"Good B,d,Done,Low,ui\n"
	jobID, _ := jobs.Create(ctx, ws.ID, team.ID, "linear_csv", []byte(mixed))
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	j := jobStatus(t, d, jobID)
	if j.Status != importer.JobPartial {
		t.Fatalf("status = %s, want partial", j.Status)
	}
	if j.Imported != 2 || j.Failed != 1 {
		t.Fatalf("counts imported=%d failed=%d, want 2/1", j.Imported, j.Failed)
	}
	if j.ErrorSummary == "" {
		t.Fatal("partial job must carry an error_summary")
	}
}

func issuePriorities(t *testing.T, d *testutil.DB, wsID string) []int {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(), `SELECT priority FROM issues WHERE workspace_id=$1`, wsID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var p int
		_ = rows.Scan(&p)
		out = append(out, p)
	}
	return out
}

// (d) END-TO-END + MAPPER SELECTION: run a linear_csv and a jira_csv job; each uses its OWN mapper. The
// distinguishing case: Priority "Blocker" → Urgent(1) under jiraRowMapper, but NOT under linearRowMapper
// (which maps only "urgent"; "blocker" → None/0). So a jira job landing priority 1 proves source_type picked
// the jira mapper.
func TestRunner_EndToEnd_SourceTypeSelectsMapper(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsL, wsJ := d.Workspace(t), d.Workspace(t)
	teamL, teamJ := d.Team(t, wsL.ID), d.Team(t, wsJ.ID)
	jobs, runner := newSpine(d)

	linCSV := "Title,Description,Status,Priority,Labels\nLin,d,Todo,Urgent,bug\n"
	jiraCSV := "Summary,Description,Status,Priority,Labels\nJir,d,Done,Blocker,bug\n"
	_, _ = jobs.Create(ctx, wsL.ID, teamL.ID, "linear_csv", []byte(linCSV))
	_, _ = jobs.Create(ctx, wsJ.ID, teamJ.ID, "jira_csv", []byte(jiraCSV))

	runner.RunOnce(ctx)
	runner.RunOnce(ctx)

	lp := issuePriorities(t, d, wsL.ID)
	if len(lp) != 1 || lp[0] != 1 {
		t.Fatalf("linear job priorities = %v, want [1] (Urgent via linear mapper)", lp)
	}
	jp := issuePriorities(t, d, wsJ.ID)
	if len(jp) != 1 || jp[0] != 1 {
		t.Fatalf("jira job priorities = %v, want [1] (Blocker→Urgent via JIRA mapper — linear would give 0)", jp)
	}
}

// (e) CONCURRENCY: two jobs in different workspaces run concurrently — no cross-workspace, no count
// corruption. -race.
func TestRunner_ConcurrentJobs_NoCrossWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws1, ws2 := d.Workspace(t), d.Workspace(t)
	t1, t2 := d.Team(t, ws1.ID), d.Team(t, ws2.ID)
	jobs, runner := newSpine(d)

	_, _ = jobs.Create(ctx, ws1.ID, t1.ID, "linear_csv", []byte(asyncLinearCSV)) // 2 issues
	_, _ = jobs.Create(ctx, ws2.ID, t2.ID, "linear_csv", []byte(asyncLinearCSV)) // 2 issues

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = runner.RunOnce(ctx) }()
	}
	wg.Wait()

	if n := issueCount(t, d, ws1.ID); n != 2 {
		t.Fatalf("ws1 got %d issues, want 2", n)
	}
	if n := issueCount(t, d, ws2.ID); n != 2 {
		t.Fatalf("ws2 got %d issues, want 2", n)
	}
}

// (g) PAYLOAD/JOB ATOMICITY: create writes job + payload in one tx (both present); ON DELETE CASCADE removes
// the payload with the job; and Get (the status poll) does NOT read the payload table (works even with the
// payload deleted).
func TestJob_PayloadAtomicityAndCascade(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	jobs := importer.NewJobStore(d.Pool)

	jobID, err := jobs.Create(ctx, ws.ID, team.ID, "linear_csv", []byte(asyncLinearCSV))
	if err != nil {
		t.Fatal(err)
	}

	count := func(table, where string) int {
		var n int
		_ = d.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+where+"=$1", jobID).Scan(&n)
		return n
	}
	if count("import_jobs", "id") != 1 || count("import_job_payloads", "job_id") != 1 {
		t.Fatal("create must write BOTH the job row and its payload (same tx)")
	}

	// The status poll must NOT depend on the payload table — delete the payload, Get still works.
	if _, err := d.Pool.Exec(ctx, `DELETE FROM import_job_payloads WHERE job_id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if j, err := jobs.Get(ctx, jobID); err != nil || j == nil {
		t.Fatalf("Get must not read the payload table (hot path off the blob): j=%v err=%v", j, err)
	}

	// ON DELETE CASCADE: deleting the job removes any remaining payload.
	if _, err := d.Pool.Exec(ctx, `DELETE FROM import_jobs WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if count("import_job_payloads", "job_id") != 0 {
		t.Fatal("ON DELETE CASCADE must remove the payload when the job is deleted")
	}
}
