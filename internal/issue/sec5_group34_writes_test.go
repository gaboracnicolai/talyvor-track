package issue_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/automation"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/guest"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workflow"
)

// SEC-5 excluded-file WRITES (the last operate-by-id holes on the paths.exclude backlog): a member of
// A deletes/revokes B's workflow status / automation rule / guest by passing B's id to an A-authorized
// route. Same class as Groups 1-2. GREEN: 404, B's row survives.
func sec5WritesChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(sec5Secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		workflow.NewHandler(workflow.New(d.Pool)).Mount(r)
		automation.NewHandler(automation.New(d.Pool, issue.NewStore(d.Pool), nil)).Mount(r)
		// Only the by-id Revoke route (the guest token routes use a separate auth model).
		gh := guest.NewHandler(guest.NewStore(d.Pool, "guest-secret"), issue.NewStore(d.Pool), "http://x")
		r.Delete("/workspaces/{wsID}/guests/{id}", gh.Revoke)
	})
	return r
}

func TestSEC5_ExcludedWrites_CrossTenant(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	sec5Member(t, d, wsA.ID, "alice@corp.com")
	sec5Member(t, d, wsB.ID, "bob@corp.com")
	teamB := d.Team(t, wsB.ID)
	projB, err := project.NewStore(d.Pool).Create(ctx, model.Project{WorkspaceID: wsB.ID, TeamID: teamB.ID, Name: "PB", Identifier: "PB"})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}

	var statusB, ruleB, guestB string
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO workflow_statuses (team_id, name, color, category, position, is_default)
         VALUES ($1, 'Blocked', '#000000', 'backlog', 9, false) RETURNING id`, teamB.ID).Scan(&statusB); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO automation_rules (workspace_id, team_id, name, enabled, trigger, conditions, actions, action_data)
         VALUES ($1, $2, 'RuleB', true, 'status_changed', '[]'::jsonb, '{}'::text[], '{}'::jsonb) RETURNING id`,
		wsB.ID, teamB.ID).Scan(&ruleB); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO guests (workspace_id, project_id, email, name, role)
         VALUES ($1, $2, 'g@x.com', 'Guest', 'commenter') RETURNING id`, wsB.ID, projB.ID).Scan(&guestB); err != nil {
		t.Fatalf("seed guest: %v", err)
	}

	chain := sec5WritesChain(d)
	code := func(r *http.Request) int { rr := httptest.NewRecorder(); chain.ServeHTTP(rr, r); return rr.Code }
	guestActive := func(id string) bool {
		var a bool
		if err := d.Pool.QueryRow(ctx, `SELECT active FROM guests WHERE id=$1`, id).Scan(&a); err != nil {
			t.Fatalf("guest active: %v", err)
		}
		return a
	}

	// Alice (member of A) acts on B's objects via A-authorized routes — each MUST 404, row survives.
	if c := code(delAs(wsA.ID, "/teams/"+teamB.ID+"/statuses/"+statusB, "alice@corp.com")); c != http.StatusNotFound {
		t.Errorf("Alice DELETE B's workflow status = %d, want 404", c)
	}
	if !rowExists(t, d, "workflow_statuses", statusB) {
		t.Errorf("B's workflow status was DELETED by a member of A")
	}

	if c := code(delAs(wsA.ID, "/automation/rules/"+ruleB, "alice@corp.com")); c != http.StatusNotFound {
		t.Errorf("Alice DELETE B's automation rule = %d, want 404", c)
	}
	if !rowExists(t, d, "automation_rules", ruleB) {
		t.Errorf("B's automation rule was DELETED by a member of A")
	}

	if c := code(delAs(wsA.ID, "/guests/"+guestB, "alice@corp.com")); c != http.StatusNotFound {
		t.Errorf("Alice REVOKE B's guest = %d, want 404", c)
	}
	if !guestActive(guestB) {
		t.Errorf("B's guest was REVOKED (active=false) by a member of A")
	}
}
