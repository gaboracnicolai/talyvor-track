package issue_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/team"
	"github.com/talyvor/track/internal/testutil"
)

// SEC-5 Group 1: destructive operate-by-id IDOR. Track routes are /v1/workspaces/{wsID}/<obj>/{id};
// the authz middleware authorizes {wsID} but never checks {id} ∈ {wsID}, and the by-id store
// methods are `WHERE id=$1` (no workspace predicate). So a member of A can DELETE B's project/
// team/issue by id via a route authorized for A.
//
// RED (pre-fix): Alice's cross-tenant deletes SUCCEED (200) and destroy B's objects.
// GREEN (post-fix): 404 and the object survives; Bob can still delete his own (scope, not a broken query).

const sec5Secret = "test-gateway-transit-secret-0123456789"

func sec5Chain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(sec5Secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		project.NewHandler(project.NewStore(d.Pool)).Mount(r)
		team.NewHandler(team.NewStore(d.Pool)).Mount(r)
		issue.NewHandler(issue.NewStore(d.Pool)).Mount(r)
	})
	return r
}

func sec5Member(t *testing.T, d *testutil.DB, wsID, email string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'member')`,
		wsID, email, email); err != nil {
		t.Fatalf("seed member: %v", err)
	}
}

// sec5Owner seeds an OWNER. Team/project delete (and workspace admin) is owner-gated, so
// tests exercising those ops through the chain need an owner caller to reach the store's
// workspace-scoping logic (a member is short-circuited by the owner gate with 403).
func sec5Owner(t *testing.T, d *testutil.DB, wsID, email string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'owner')`,
		wsID, email, email); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
}

func delAs(wsID, subpath, email string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/v1/workspaces/"+wsID+subpath, nil)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, sec5Secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req
}

func patchAs(wsID, subpath, email, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, "/v1/workspaces/"+wsID+subpath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gatewayauth.HeaderGatewayAuth, sec5Secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req
}

func rowExists(t *testing.T, d *testutil.DB, table, id string) bool {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+table+` WHERE id=$1`, id).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n > 0
}

func issueStatus(t *testing.T, d *testutil.DB, id string) string {
	t.Helper()
	var s string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT status FROM issues WHERE id=$1`, id).Scan(&s); err != nil {
		t.Fatalf("issue status: %v", err)
	}
	return s
}

func TestSEC5_Group1_CrossTenantDelete(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	// Owners of their own workspace ONLY — team/project delete is owner-gated, so an owner
	// caller is needed to reach the store's cross-tenant scoping (still 404 across tenants).
	sec5Owner(t, d, wsA.ID, "alice@corp.com") // Alice → A only
	sec5Owner(t, d, wsB.ID, "bob@corp.com")   // Bob → B only

	// teamB is a PARENT for the project/issue objects (never itself deleted — a team with
	// children can't be hard-deleted regardless of auth, so team-delete uses bare teams below).
	teamB := d.Team(t, wsB.ID)
	issueB := d.Issue(t, wsB.ID, teamB.ID)  // Alice's cross-tenant issue-delete target
	issueB2 := d.Issue(t, wsB.ID, teamB.ID) // Bob's own issue-delete
	projB, err := project.NewStore(d.Pool).Create(ctx, model.Project{
		WorkspaceID: wsB.ID, TeamID: teamB.ID, Name: "B Project", Identifier: "BPROJ",
	})
	if err != nil {
		t.Fatalf("seed project B: %v", err)
	}
	projB2, err := project.NewStore(d.Pool).Create(ctx, model.Project{
		WorkspaceID: wsB.ID, TeamID: teamB.ID, Name: "B Project 2", Identifier: "BPROJ2",
	})
	if err != nil {
		t.Fatalf("seed project B2: %v", err)
	}
	// Bare teams (no children) so a hard DELETE would actually succeed if authz allowed it.
	teamDelAlice := d.Team(t, wsB.ID) // Alice's cross-tenant team-delete target
	teamDelBob := d.Team(t, wsB.ID)   // Bob's own team-delete

	chain := sec5Chain(d)
	do := func(r *http.Request) int {
		rr := httptest.NewRecorder()
		chain.ServeHTTP(rr, r)
		return rr.Code
	}

	// (a) Alice (member A) → DELETE B's project via a route authorized for A → must 404, survive.
	if code := do(delAs(wsA.ID, "/projects/"+projB.ID, "alice@corp.com")); code != http.StatusNotFound {
		t.Errorf("(a) Alice DELETE B's project = %d, want 404 (cross-tenant)", code)
	}
	if !rowExists(t, d, "projects", projB.ID) {
		t.Errorf("(a) B's project was DESTROYED by a member of A — cross-tenant delete")
	}

	// No existence oracle: a NONEXISTENT id returns the SAME 404 as a foreign id — the
	// not-found sentinel never reveals whether the object exists in another workspace.
	if code := do(delAs(wsA.ID, "/projects/00000000-0000-0000-0000-000000000000", "alice@corp.com")); code != http.StatusNotFound {
		t.Errorf("nonexistent project id = %d, want 404 (same as foreign — no existence oracle)", code)
	}

	// (b) Alice → DELETE B's (bare) team → 404, survive.
	if code := do(delAs(wsA.ID, "/teams/"+teamDelAlice.ID, "alice@corp.com")); code != http.StatusNotFound {
		t.Errorf("(b) Alice DELETE B's team = %d, want 404", code)
	}
	if !rowExists(t, d, "teams", teamDelAlice.ID) {
		t.Errorf("(b) B's team was DESTROYED by a member of A")
	}

	// (c) Alice → DELETE (soft-cancel) B's issue → 404, still active.
	if code := do(delAs(wsA.ID, "/issues/"+issueB.ID, "alice@corp.com")); code != http.StatusNotFound {
		t.Errorf("(c) Alice DELETE B's issue = %d, want 404", code)
	}
	if s := issueStatus(t, d, issueB.ID); s == "cancelled" {
		t.Errorf("(c) B's issue was CANCELLED by a member of A (status=%s)", s)
	}

	// Folded-in Update scoping: Alice PATCH B's project/team → 404, and the object is not
	// mutated (projB survived her blocked delete; teamB is B's parent team).
	if code := do(patchAs(wsA.ID, "/projects/"+projB.ID, "alice@corp.com", `{"name":"hacked"}`)); code != http.StatusNotFound {
		t.Errorf("Alice PATCH B's project = %d, want 404 (cross-tenant update)", code)
	}
	if code := do(patchAs(wsA.ID, "/teams/"+teamB.ID, "alice@corp.com", `{"name":"hacked"}`)); code != http.StatusNotFound {
		t.Errorf("Alice PATCH B's team = %d, want 404 (cross-tenant update)", code)
	}

	// (d) SCOPE-SOURCE: Bob (member B) deletes his OWN objects → 200. Proves the denials above
	// are the workspace mismatch, not a globally-broken query.
	if code := do(delAs(wsB.ID, "/projects/"+projB2.ID, "bob@corp.com")); code != http.StatusOK {
		t.Errorf("(d) Bob DELETE own project = %d, want 200 (over-blocked)", code)
	}
	if code := do(delAs(wsB.ID, "/teams/"+teamDelBob.ID, "bob@corp.com")); code != http.StatusOK {
		t.Errorf("(d) Bob DELETE own team = %d, want 200", code)
	}
	if code := do(delAs(wsB.ID, "/issues/"+issueB2.ID, "bob@corp.com")); code != http.StatusOK {
		t.Errorf("(d) Bob DELETE own issue = %d, want 200", code)
	}
}
