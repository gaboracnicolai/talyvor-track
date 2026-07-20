package issue_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/customfield"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/label"
	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/template"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/timetracking"
)

// SEC-5 Group 2: the remaining destructive deletes + mutations, same class as Group 1 — a member
// of workspace A acts on B's object by passing B's id to an A-authorized route because the by-id
// store query is `WHERE id=$1` with no workspace predicate.
//
// RED: every Alice cross-tenant action succeeds today. GREEN: each 404s, object survives/unchanged;
// Bob acts on his own; nonexistent id → same 404 as foreign (no oracle).
func sec5Group2Chain(d *testutil.DB) http.Handler {
	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(sec5Secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		label.NewHandler(label.NewStore(d.Pool)).Mount(r)
		template.NewHandler(template.NewStore(d.Pool)).Mount(r)
		customfield.NewHandler(customfield.NewStore(d.Pool)).Mount(r)
		timetracking.NewHandler(timetracking.NewStore(d.Pool)).Mount(r)
		milestone.NewHandler(milestone.NewStore(d.Pool)).Mount(r)
		cycle.NewHandler(cycle.NewStore(d.Pool)).Mount(r)
		issue.NewHandler(issue.NewStore(d.Pool)).Mount(r)
	})
	return r
}

func memberID(t *testing.T, d *testutil.DB, wsID, email string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT id FROM members WHERE workspace_id=$1 AND email=$2`, wsID, email).Scan(&id); err != nil {
		t.Fatalf("member id: %v", err)
	}
	return id
}

func TestSEC5_Group2_CrossTenant(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	sec5Member(t, d, wsA.ID, "alice@corp.com")
	sec5Member(t, d, wsB.ID, "bob@corp.com")
	bobID := memberID(t, d, wsB.ID, "bob@corp.com")

	teamB := d.Team(t, wsB.ID)
	issueB := d.Issue(t, wsB.ID, teamB.ID)
	projB, err := project.NewStore(d.Pool).Create(ctx, model.Project{WorkspaceID: wsB.ID, TeamID: teamB.ID, Name: "PB", Identifier: "PB"})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	labelB, err := label.NewStore(d.Pool).Create(ctx, label.Label{WorkspaceID: wsB.ID, Name: "bug"})
	if err != nil {
		t.Fatalf("seed label: %v", err)
	}
	tmplB, err := template.NewStore(d.Pool).Create(ctx, template.IssueTemplate{WorkspaceID: wsB.ID, Name: "Tmpl"})
	if err != nil {
		t.Fatalf("seed template: %v", err)
	}
	fieldB := d.CustomField(t, wsB.ID, "Severity")
	teB, err := timetracking.NewStore(d.Pool).StartTimer(ctx, issueB.ID, wsB.ID, bobID, "work")
	if err != nil {
		t.Fatalf("seed time entry: %v", err)
	}
	mB, err := milestone.NewStore(d.Pool).Create(ctx, milestone.Milestone{WorkspaceID: wsB.ID, ProjectID: projB.ID, Name: "MB"})
	if err != nil {
		t.Fatalf("seed milestone: %v", err)
	}
	cyB, err := cycle.NewStore(d.Pool).Create(ctx, model.Cycle{WorkspaceID: wsB.ID, TeamID: teamB.ID, Name: "CB", StartDate: time.Now(), EndDate: time.Now().Add(7 * 24 * time.Hour)})
	if err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	// Authored BY Bob so "Bob deletes his own comment" is literally true under the new
	// author-or-owner rule; Alice's cross-tenant attempt is still blocked by workspace scope.
	cmtB := &model.Comment{ID: seedCommentBy(t, d, issueB.ID, bobID, "secret")}

	chain := sec5Group2Chain(d)
	do := func(r *http.Request) int { rr := httptest.NewRecorder(); chain.ServeHTTP(rr, r); return rr.Code }
	postAs := func(wsID, subpath, email string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+wsID+subpath, nil)
		req.Header.Set(gatewayauth.HeaderGatewayAuth, sec5Secret)
		req.Header.Set(gatewayauth.HeaderUserEmail, email)
		return req
	}

	// ── Alice's cross-tenant actions (via A-authorized routes) — each must 404 ──
	type tc struct {
		name, table, id string
		code            int
	}
	// Destructive deletes → 404, object survives.
	dels := []tc{
		{"label", "labels", labelB.ID, do(delAs(wsA.ID, "/labels/"+labelB.ID, "alice@corp.com"))},
		{"template", "issue_templates", tmplB.ID, do(delAs(wsA.ID, "/templates/"+tmplB.ID, "alice@corp.com"))},
		{"customfield", "custom_fields", fieldB.ID, do(delAs(wsA.ID, "/custom-fields/"+fieldB.ID, "alice@corp.com"))},
		{"timetracking", "time_entries", teB.ID, do(delAs(wsA.ID, "/time-entries/"+teB.ID, "alice@corp.com"))},
	}
	for _, c := range dels {
		if c.code != http.StatusNotFound {
			t.Errorf("Alice DELETE B's %s = %d, want 404 (cross-tenant)", c.name, c.code)
		}
		if !rowExists(t, d, c.table, c.id) {
			t.Errorf("B's %s was DESTROYED by a member of A", c.name)
		}
	}

	// Mutations → 404, object unchanged.
	if code := do(patchAs(wsA.ID, "/issues/"+issueB.ID, "alice@corp.com", `{"title":"hacked"}`)); code != http.StatusNotFound {
		t.Errorf("Alice PATCH B's issue = %d, want 404", code)
	}
	if code := do(patchAs(wsA.ID, "/projects/"+projB.ID+"/milestones/"+mB.ID, "alice@corp.com", `{"name":"hacked"}`)); code != http.StatusNotFound {
		t.Errorf("Alice PATCH B's milestone = %d, want 404", code)
	}
	if code := do(patchAs(wsA.ID, "/templates/"+tmplB.ID, "alice@corp.com", `{"name":"hacked"}`)); code != http.StatusNotFound {
		t.Errorf("Alice PATCH B's template = %d, want 404", code)
	}
	if code := do(postAs(wsA.ID, "/teams/"+teamB.ID+"/cycles/"+cyB.ID+"/complete", "alice@corp.com")); code != http.StatusNotFound {
		t.Errorf("Alice COMPLETE B's cycle = %d, want 404", code)
	}
	// Issue-comment mutation → 404 (join scope).
	if code := do(delAs(wsA.ID, "/issues/"+issueB.ID+"/comments/"+cmtB.ID, "alice@corp.com")); code != http.StatusNotFound {
		t.Errorf("Alice DELETE B's comment = %d, want 404", code)
	}
	if !rowExists(t, d, "comments", cmtB.ID) {
		t.Errorf("B's comment was DELETED by a member of A")
	}

	// No existence oracle: a nonexistent id → same 404 as a foreign id.
	if code := do(delAs(wsA.ID, "/labels/00000000-0000-0000-0000-000000000000", "alice@corp.com")); code != http.StatusNotFound {
		t.Errorf("nonexistent label id = %d, want 404 (no oracle)", code)
	}

	// ── SCOPE-SOURCE: Bob acting on his OWN objects still succeeds (denial = mismatch, not broken query) ──
	if code := do(delAs(wsB.ID, "/labels/"+labelB.ID, "bob@corp.com")); code != http.StatusOK {
		t.Errorf("Bob DELETE own label = %d, want 200 (over-blocked)", code)
	}
	if code := do(delAs(wsB.ID, "/time-entries/"+teB.ID, "bob@corp.com")); code != http.StatusOK {
		t.Errorf("Bob DELETE own time entry = %d, want 200", code)
	}
	if code := do(postAs(wsB.ID, "/teams/"+teamB.ID+"/cycles/"+cyB.ID+"/complete", "bob@corp.com")); code != http.StatusOK {
		t.Errorf("Bob COMPLETE own cycle = %d, want 200", code)
	}
	if code := do(delAs(wsB.ID, "/issues/"+issueB.ID+"/comments/"+cmtB.ID, "bob@corp.com")); code != http.StatusOK {
		t.Errorf("Bob DELETE own comment = %d, want 200", code)
	}
}
