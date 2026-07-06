package issue_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/team"
	"github.com/talyvor/track/internal/template"
	"github.com/talyvor/track/internal/testutil"
)

// SEC-5 Group 3: by-id READS. A member of workspace A GETs B's object by passing B's id to an
// A-authorized route — the store read is `WHERE id=$1` with no workspace predicate, so it returns
// B's row (cross-tenant disclosure). GREEN: 404 (foreign id and nonexistent id indistinguishable).
func sec5ReadsChain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(sec5Secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		project.NewHandler(project.NewStore(d.Pool)).Mount(r)
		team.NewHandler(team.NewStore(d.Pool)).Mount(r)
		issue.NewHandler(issue.NewStore(d.Pool)).Mount(r)
		template.NewHandler(template.NewStore(d.Pool)).Mount(r)
		milestone.NewHandler(milestone.NewStore(d.Pool)).Mount(r)
		cycle.NewHandler(cycle.NewStore(d.Pool)).Mount(r)
	})
	return r
}

func getAs(wsID, subpath, email string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+wsID+subpath, nil)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, sec5Secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	return req
}

func TestSEC5_Group3_CrossTenantReads(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	sec5Member(t, d, wsA.ID, "alice@corp.com")
	sec5Member(t, d, wsB.ID, "bob@corp.com")

	teamB := d.Team(t, wsB.ID)
	issueB := d.Issue(t, wsB.ID, teamB.ID)
	projB, err := project.NewStore(d.Pool).Create(ctx, model.Project{WorkspaceID: wsB.ID, TeamID: teamB.ID, Name: "PB", Identifier: "PB"})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	tmplB, err := template.NewStore(d.Pool).Create(ctx, template.IssueTemplate{WorkspaceID: wsB.ID, Name: "TmplB"})
	if err != nil {
		t.Fatalf("seed template: %v", err)
	}
	mB, err := milestone.NewStore(d.Pool).Create(ctx, milestone.Milestone{WorkspaceID: wsB.ID, ProjectID: projB.ID, Name: "MB"})
	if err != nil {
		t.Fatalf("seed milestone: %v", err)
	}
	cyB, err := cycle.NewStore(d.Pool).Create(ctx, model.Cycle{WorkspaceID: wsB.ID, TeamID: teamB.ID, Name: "CB", StartDate: time.Now(), EndDate: time.Now().Add(7 * 24 * time.Hour)})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}

	chain := sec5ReadsChain(d)
	code := func(r *http.Request) int { rr := httptest.NewRecorder(); chain.ServeHTTP(rr, r); return rr.Code }

	// Alice (member of A) reads B's objects via A-authorized routes — each MUST 404 (not 200+leak).
	reads := []struct {
		name, path string
	}{
		{"project", "/projects/" + projB.ID},
		{"team", "/teams/" + teamB.ID},
		{"issue", "/issues/" + issueB.ID},
		{"template", "/templates/" + tmplB.ID},
	}
	for _, rd := range reads {
		if c := code(getAs(wsA.ID, rd.path, "alice@corp.com")); c != http.StatusNotFound {
			t.Errorf("Alice GET B's %s = %d, want 404 (cross-tenant read leak)", rd.name, c)
		}
	}

	// Derived by-id reads (progress / burndown) must ALSO 404 cross-tenant, not return B's aggregates.
	derived := []struct{ name, path string }{
		{"milestone progress", "/projects/" + projB.ID + "/milestones/" + mB.ID + "/progress"},
		{"cycle progress", "/teams/" + teamB.ID + "/cycles/" + cyB.ID + "/progress"},
		{"cycle burndown", "/teams/" + teamB.ID + "/cycles/" + cyB.ID + "/burndown"},
	}
	for _, rd := range derived {
		if c := code(getAs(wsA.ID, rd.path, "alice@corp.com")); c != http.StatusNotFound {
			t.Errorf("Alice GET B's %s = %d, want 404 (derived read leak)", rd.name, c)
		}
	}

	// No existence oracle: a nonexistent id → same 404 as a foreign id.
	if c := code(getAs(wsA.ID, "/projects/00000000-0000-0000-0000-000000000000", "alice@corp.com")); c != http.StatusNotFound {
		t.Errorf("nonexistent project id = %d, want 404 (no oracle)", c)
	}

	// SCOPE-SOURCE: Bob reading his OWN objects still succeeds (denial would be over-blocking).
	if c := code(getAs(wsB.ID, "/projects/"+projB.ID, "bob@corp.com")); c != http.StatusOK {
		t.Errorf("Bob GET own project = %d, want 200 (over-blocked)", c)
	}
	if c := code(getAs(wsB.ID, "/issues/"+issueB.ID, "bob@corp.com")); c != http.StatusOK {
		t.Errorf("Bob GET own issue = %d, want 200", c)
	}
}
