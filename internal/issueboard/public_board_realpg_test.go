package issueboard_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/issueboard"
	"github.com/talyvor/track/internal/testutil"
)

// router mounts the handler as main.go does, with the caller's authorized workspace and role put in
// context the way the {wsID} middleware would. role "" is a signed-out caller.
func router(h *issueboard.Handler, wsID, role string) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if role != "" {
				req = req.WithContext(authz.WithAuthorizedRole(req.Context(), wsID, "member-"+role, role))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Route("/v1", h.Mount)
	return r
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestPublicBoard_OnReadOff_RealPG — an owner turns a link on, a signed-out reader sees the
// workspace's issues and only them, a member cannot publish, and turning the link off ends it.
func TestPublicBoard_OnReadOff_RealPG(t *testing.T) {
	d := testutil.New(t)
	ws, other := d.Workspace(t), d.Workspace(t)
	mine := d.Issue(t, ws.ID, "")
	theirs := d.Issue(t, other.ID, "")
	h := issueboard.NewHandler(issueboard.NewStore(d.Pool))

	if rec := do(t, router(h, ws.ID, authz.RoleMember), http.MethodPost, "/v1/workspaces/"+ws.ID+"/issue-boards", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("a non-owner member published a board: %d %s", rec.Code, rec.Body)
	}

	owner := router(h, ws.ID, authz.RoleOwner)
	rec := do(t, owner, http.MethodPost, "/v1/workspaces/"+ws.ID+"/issue-boards", `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner create: %d %s", rec.Code, rec.Body)
	}
	var share issueboard.Share
	if err := json.Unmarshal(rec.Body.Bytes(), &share); err != nil || share.Token == "" {
		t.Fatalf("create answered no token: %v %s", err, rec.Body)
	}

	anon := router(h, "", "")
	rec = do(t, anon, http.MethodGet, "/v1/public/issue-boards/"+share.Token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("signed-out read: %d %s", rec.Code, rec.Body)
	}
	var raw struct {
		Workspace string           `json:"workspace"`
		Issues    []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Workspace != ws.Name || len(raw.Issues) != 1 || raw.Issues[0]["identifier"] != mine.Identifier ||
		raw.Issues[0]["title"] != mine.Title {
		t.Fatalf("board = %s; want only %s %q of %q (and never %s)", rec.Body, mine.Identifier, mine.Title, ws.Name, theirs.Identifier)
	}
	var keys []string
	for k := range raw.Issues[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if got := strings.Join(keys, ","); got != "identifier,priority,status,title,updated_at" {
		t.Fatalf("a public issue carries %s — anything beyond identifier, title, status, priority and updated_at is published to strangers", got)
	}

	rec = do(t, router(h, ws.ID, authz.RoleMember), http.MethodGet, "/v1/workspaces/"+ws.ID+"/issue-boards", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), share.Token) {
		t.Fatalf("a member cannot see the workspace's live link: %d %s", rec.Code, rec.Body)
	}

	if rec := do(t, owner, http.MethodDelete, "/v1/workspaces/"+ws.ID+"/issue-boards/"+share.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("owner revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, anon, http.MethodGet, "/v1/public/issue-boards/"+share.Token, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a revoked link still answers: %d %s", rec.Code, rec.Body)
	}
}

// TestPublicBoard_ProjectLinkShowsThatProject_RealPG — a link made for one project shows only it,
// and a project from another workspace is refused.
func TestPublicBoard_ProjectLinkShowsThatProject_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws, other := d.Workspace(t), d.Workspace(t)
	team := d.Team(t, ws.ID)
	var projectID, foreignProject string
	if err := d.Pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, team_id, name, identifier)
        VALUES ($1, $2, 'Website', 'WEB') RETURNING id`, ws.ID, team.ID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	otherTeam := d.Team(t, other.ID)
	if err := d.Pool.QueryRow(ctx, `INSERT INTO projects (workspace_id, team_id, name, identifier)
        VALUES ($1, $2, 'Theirs', 'THR') RETURNING id`, other.ID, otherTeam.ID).Scan(&foreignProject); err != nil {
		t.Fatal(err)
	}
	inProject := d.Issue(t, ws.ID, team.ID)
	if _, err := d.Pool.Exec(ctx, `UPDATE issues SET project_id = $1 WHERE id = $2`, projectID, inProject.ID); err != nil {
		t.Fatal(err)
	}
	d.Issue(t, ws.ID, team.ID) // outside the project

	s := issueboard.NewStore(d.Pool)
	if _, err := s.Create(ctx, ws.ID, &foreignProject, "m"); err == nil {
		t.Fatal("a link was made for another workspace's project")
	}
	share, err := s.Create(ctx, ws.ID, &projectID, "m")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Public(ctx, share.Token)
	if err != nil {
		t.Fatal(err)
	}
	if b.Project != "Website" || len(b.Issues) != 1 || b.Issues[0].Identifier != inProject.Identifier {
		t.Fatalf("project board = %+v; want only %s under Website", b, inProject.Identifier)
	}
}
