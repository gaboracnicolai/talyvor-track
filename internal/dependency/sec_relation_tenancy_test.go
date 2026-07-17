package dependency_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/dependency"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/testutil"
)

const depSecret = "test-gateway-transit-secret-0123456789"

func depSeedMember(t *testing.T, d *testutil.DB, wsID, email string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'member')`,
		wsID, email, email); err != nil {
		t.Fatalf("seed member: %v", err)
	}
}

func depChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(depSecret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		dependency.NewHandler(dependency.NewStore(d.Pool)).Mount(r)
	})
	return r
}

func depPost(wsID, issueID, subpath, email, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost,
		"/v1/workspaces/"+wsID+"/issues/"+issueID+"/relations"+subpath, bytes.NewReader([]byte(body)))
	req.Header.Set(gatewayauth.HeaderGatewayAuth, depSecret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func relCount(t *testing.T, d *testutil.DB, sourceID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM issue_relations WHERE source_id=$1 OR target_id=$1`, sourceID).Scan(&n); err != nil {
		t.Fatalf("count relations: %v", err)
	}
	return n
}

// LIVE cross-tenant write #1 — dependency.Create. The guard assertIssuesShareWorkspace
// required source & target to share EACH OTHER's workspace, never the caller's authorized
// one. A member of wsA could forge a dependency edge between TWO wsB issues (they share wsB,
// so the guard passed); it stamps workspace_id=wsA but references wsB issues and surfaces in
// wsB's own issue views (GetRelations/GetBlockedBy scope by the perspective issue's ws).
// RED on main: 201, rows land. GREEN: refused, 0 rows.
func TestSEC_CreateRelation_CrossWorkspace_Rejected(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	depSeedMember(t, d, wsA.ID, "alice@corp.com") // member of wsA only
	teamB := d.Team(t, wsB.ID)
	issueB1 := d.Issue(t, wsB.ID, teamB.ID)
	issueB2 := d.Issue(t, wsB.ID, teamB.ID)

	rr := httptest.NewRecorder()
	depChain(d).ServeHTTP(rr, depPost(wsA.ID, issueB1.ID, "", "alice@corp.com",
		`{"target_id":"`+issueB2.ID+`","type":"blocks"}`))

	if rr.Code == http.StatusCreated {
		t.Errorf("LIVE IDOR: wsA member forged a relation between two wsB issues (HTTP %d)", rr.Code)
	}
	if n := relCount(t, d, issueB1.ID); n != 0 {
		t.Errorf("a relation touching wsB's issue was written (%d rows) — cross-tenant write not prevented", n)
	}
}

// LIVE cross-tenant write #2 — dependency.BulkCreateRelations. badTargets bound the targets
// to the SOURCE issue's workspace, and the source was never bound to the caller's authorized
// workspace. Same forge, in bulk. RED on main: rows land. GREEN: refused, 0 rows.
func TestSEC_BulkCreateRelations_CrossWorkspace_Rejected(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	depSeedMember(t, d, wsA.ID, "alice@corp.com")
	teamB := d.Team(t, wsB.ID)
	issueB1 := d.Issue(t, wsB.ID, teamB.ID)
	issueB2 := d.Issue(t, wsB.ID, teamB.ID)

	rr := httptest.NewRecorder()
	depChain(d).ServeHTTP(rr, depPost(wsA.ID, issueB1.ID, "/bulk", "alice@corp.com",
		`{"target_ids":["`+issueB2.ID+`"],"type":"blocks"}`))

	if rr.Code == http.StatusCreated || rr.Code == http.StatusOK {
		t.Errorf("LIVE IDOR (bulk): wsA member forged relations onto wsB issues (HTTP %d): %s", rr.Code, rr.Body.String())
	}
	if n := relCount(t, d, issueB1.ID); n != 0 {
		t.Errorf("bulk wrote a relation touching wsB's issue (%d rows) — cross-tenant write not prevented", n)
	}
}

// Same-workspace happy paths — the fix must not break legitimate creates.
func TestSEC_CreateRelation_SameWorkspace_Succeeds(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	depSeedMember(t, d, wsA.ID, "alice@corp.com")
	teamA := d.Team(t, wsA.ID)
	a1 := d.Issue(t, wsA.ID, teamA.ID)
	a2 := d.Issue(t, wsA.ID, teamA.ID)

	rr := httptest.NewRecorder()
	depChain(d).ServeHTTP(rr, depPost(wsA.ID, a1.ID, "", "alice@corp.com",
		`{"target_id":"`+a2.ID+`","type":"blocks"}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("same-workspace Create must be 201; got %d: %s", rr.Code, rr.Body.String())
	}

	rr2 := httptest.NewRecorder()
	b1 := d.Issue(t, wsA.ID, teamA.ID)
	b2 := d.Issue(t, wsA.ID, teamA.ID)
	depChain(d).ServeHTTP(rr2, depPost(wsA.ID, b1.ID, "/bulk", "alice@corp.com",
		`{"target_ids":["`+b2.ID+`"],"type":"blocks"}`))
	if rr2.Code != http.StatusOK && rr2.Code != http.StatusCreated {
		t.Fatalf("same-workspace BulkCreate must succeed; got %d: %s", rr2.Code, rr2.Body.String())
	}
}
