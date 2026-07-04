package importer_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/importer"
	"github.com/talyvor/track/internal/integrations"
	"github.com/talyvor/track/internal/testutil"
)

// ── T8 C.4 — *_api enqueue proofs (real PG). Reuses secret/seedMember from jobs_integration_test.go. ──

func testCipherStore(t *testing.T, d *testutil.DB) *integrations.Store {
	t.Helper()
	c, err := integrations.NewCipher(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	return integrations.NewStore(d.Pool, c)
}

// apiJobChain mounts the async JobHandler WITH the integration checker (live API enqueue enabled).
func apiJobChain(t *testing.T, d *testutil.DB) (http.Handler, *integrations.Store) {
	istore := testCipherStore(t, d)
	h := importer.NewJobHandler(importer.NewJobStore(d.Pool)).WithIntegrationChecker(istore)
	return mountJobs(d, h), istore
}

// disabledJobChain mounts the handler WITHOUT the checker (integrations disabled).
func disabledJobChain(d *testutil.DB) http.Handler {
	return mountJobs(d, importer.NewJobHandler(importer.NewJobStore(d.Pool)))
}

func mountJobs(d *testutil.DB, h *importer.JobHandler) http.Handler {
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		h.Mount(r)
	})
	return r
}

func apiEnqueue(wsID, teamID, sourceType, email string) *http.Request {
	req := httptest.NewRequest("POST",
		"/v1/import/jobs?workspace_id="+wsID+"&team_id="+teamID+"&source_type="+sourceType, nil)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req
}

func jobRowCount(t *testing.T, d *testutil.DB, wsID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM import_jobs WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// (a) HAPPY PATH: with a seeded integration, POST linear_api (no file) → 202 + job_id; an import_jobs row
// exists (linear_api, workspace A) with NO import_job_payloads row.
func TestAPIEnqueue_HappyPath(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	seedMember(t, d, ws.ID, "a@corp.com")
	h, istore := apiJobChain(t, d)
	if _, err := istore.Upsert(ctx, ws.ID, "linear", "tok", "KEY", "https://x"); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, apiEnqueue(ws.ID, team.ID, "linear_api", "a@corp.com"))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("enqueue = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "job_id") {
		t.Fatalf("202 body must carry job_id: %s", rr.Body.String())
	}

	var jobs, payloads int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM import_jobs WHERE workspace_id=$1 AND source_type='linear_api'`, ws.ID).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("import_jobs (linear_api) rows = %d, want 1", jobs)
	}
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM import_job_payloads p JOIN import_jobs j ON j.id=p.job_id WHERE j.workspace_id=$1`, ws.ID).Scan(&payloads)
	if payloads != 0 {
		t.Fatalf("an API-source job must have NO payload row, got %d", payloads)
	}
}

// (b) FAIL-FAST NO-INTEGRATION: POST linear_api for a workspace with no linear integration → 400, no job row.
func TestAPIEnqueue_NoIntegration_400(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	seedMember(t, d, ws.ID, "a@corp.com")
	h, _ := apiJobChain(t, d) // no integration seeded

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, apiEnqueue(ws.ID, team.ID, "linear_api", "a@corp.com"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("no-integration enqueue = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "integration") {
		t.Fatalf("error should explain the missing integration: %s", rr.Body.String())
	}
	if n := jobRowCount(t, d, ws.ID); n != 0 {
		t.Fatalf("no-integration enqueue wrote %d job rows, want 0", n)
	}
}

// (c) TENANCY (the gate): a member of A cannot enqueue for B (403); and the existence check for A never reads
// B's integrations — B has linear configured, A does not, so A's enqueue is 400 NO_INTEGRATION (not a false
// 202 off B's integration).
func TestAPIEnqueue_Tenancy(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA, teamB := d.Team(t, wsA.ID), d.Team(t, wsB.ID)
	seedMember(t, d, wsA.ID, "a@corp.com") // member of A only
	h, istore := apiJobChain(t, d)
	if _, err := istore.Upsert(ctx, wsB.ID, "linear", "tok", "KEY", "https://x"); err != nil { // B has it, A doesn't
		t.Fatal(err)
	}

	// member-of-A enqueues for A → the check is scoped to A (no integration) → 400, NOT a 202 off B's config.
	rrA := httptest.NewRecorder()
	h.ServeHTTP(rrA, apiEnqueue(wsA.ID, teamA.ID, "linear_api", "a@corp.com"))
	if rrA.Code != http.StatusBadRequest {
		t.Fatalf("A's enqueue (A has no integration) = %d, want 400 — the check must not read B's; body=%s", rrA.Code, rrA.Body.String())
	}

	// member-of-A enqueues for B → authz blocks (403), nothing written into B.
	rrB := httptest.NewRecorder()
	h.ServeHTTP(rrB, apiEnqueue(wsB.ID, teamB.ID, "linear_api", "a@corp.com"))
	if rrB.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant enqueue = %d, want 403; body=%s", rrB.Code, rrB.Body.String())
	}
	if n := jobRowCount(t, d, wsB.ID); n != 0 {
		t.Fatalf("cross-tenant enqueue wrote %d job rows into B", n)
	}
}

// (d) TEAM GUARD: an *_api job whose team_id is from another workspace → 400, zero rows (the AssertRefInWorkspace
// guard, same as Build B).
func TestAPIEnqueue_TeamGuard_400(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID) // team in the OTHER workspace
	seedMember(t, d, wsA.ID, "a@corp.com")
	h, istore := apiJobChain(t, d)
	if _, err := istore.Upsert(ctx, wsA.ID, "linear", "tok", "KEY", "https://x"); err != nil { // A has the integration
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, apiEnqueue(wsA.ID, teamB.ID, "linear_api", "a@corp.com"))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("cross-workspace team enqueue = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if n := jobRowCount(t, d, wsA.ID); n != 0 {
		t.Fatalf("cross-workspace-team enqueue wrote %d job rows into A", n)
	}
}

// (f) INTEGRATIONS-DISABLED: no encryption key (no checker wired) → *_api enqueue → clean 409, no panic.
func TestAPIEnqueue_IntegrationsDisabled_409(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	seedMember(t, d, ws.ID, "a@corp.com")
	h := disabledJobChain(d)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, apiEnqueue(ws.ID, team.ID, "linear_api", "a@corp.com"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("integrations-disabled enqueue = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if n := jobRowCount(t, d, ws.ID); n != 0 {
		t.Fatalf("disabled enqueue wrote %d job rows", n)
	}
}
