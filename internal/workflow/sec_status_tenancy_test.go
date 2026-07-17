package workflow_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workflow"
)

const wfSecret = "test-gateway-transit-secret-0123456789"

func wfSeedMember(t *testing.T, d *testutil.DB, wsID, email string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'member')`,
		wsID, email, email); err != nil {
		t.Fatalf("seed member: %v", err)
	}
}

func wfChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(wfSecret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		workflow.NewHandler(workflow.New(d.Pool)).Mount(r)
	})
	return r
}

func wfPost(wsID, teamID, email, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost,
		"/v1/workspaces/"+wsID+"/teams/"+teamID+"/statuses", bytes.NewReader([]byte(body)))
	req.Header.Set(gatewayauth.HeaderGatewayAuth, wfSecret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// LIVE cross-tenant write: CreateStatus trusted team_id alone, and the /v1 Create handler
// authorized {wsID} but never verified {teamID} belongs to it. A member of wsA could insert
// a workflow status onto wsB's team (workflow_statuses has no workspace_id; scope via teams).
// RED on main: 201, row lands. GREEN after the fix: 404 (no-oracle), 0 rows.
func TestSEC_CreateStatus_CrossWorkspace_Rejected(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	wfSeedMember(t, d, wsA.ID, "alice@corp.com") // member of wsA only
	teamB := d.Team(t, wsB.ID)                    // team in wsB

	rr := httptest.NewRecorder()
	wfChain(d).ServeHTTP(rr, wfPost(wsA.ID, teamB.ID, "alice@corp.com",
		`{"name":"Injected","category":"unstarted"}`))

	if rr.Code == http.StatusCreated {
		t.Errorf("LIVE IDOR: wsA member created a workflow status on wsB's team (HTTP %d)", rr.Code)
	}
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-tenant CreateStatus must be 404 (no-oracle); got %d: %s", rr.Code, rr.Body.String())
	}
	var n int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM workflow_statuses WHERE team_id=$1 AND name='Injected'`, teamB.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("a status landed on wsB's team (%d rows) — cross-tenant write not prevented", n)
	}
}

// Same-workspace happy path — the fix must not break a legitimate create.
func TestSEC_CreateStatus_SameWorkspace_Succeeds(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	wfSeedMember(t, d, wsA.ID, "alice@corp.com")
	teamA := d.Team(t, wsA.ID)

	rr := httptest.NewRecorder()
	wfChain(d).ServeHTTP(rr, wfPost(wsA.ID, teamA.ID, "alice@corp.com",
		`{"name":"Blocked","category":"unstarted"}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("same-workspace CreateStatus must be 201; got %d: %s", rr.Code, rr.Body.String())
	}
}
