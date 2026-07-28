package workspace_test

// THE FIRST ISSUE — a brand-new identity signs in and writes something down.
//
// This failed on the live deploy, from the browser's network tab:
//
//	{"error":"issue: WorkspaceID, TeamID, Title, and CreatorID are required","code":"CREATE_FAILED"}
//
// The gateway supplies WorkspaceID and CreatorID (both server-derived in issue.Handler.Create, from
// the authorized workspace and the resolved member). Title comes from the person. TeamID had NO
// SOURCE: CreateWithOwner made a workspace and its owner member and nothing else, so a freshly
// bootstrapped workspace held ZERO teams — and issues.team_id is NOT NULL REFERENCES teams(id).
// The product's primary write was therefore unreachable for every new user, on every deployment.
//
// ⚠ WHY THIS TEST IS HERE AND NOT AGAINST A STUB. A create test against a fake Track passes while
// production answers CREATE_FAILED — the failure lives in the real validator and the real schema, so
// only the real schema can witness it. testutil.New applies the production migrations to a real
// Postgres and FAILS rather than skips without a database, so this cannot go quietly green.
//
// ⚠ IT ASSERTS THE SEQUENCE, NOT THE ROW. "A default team exists" is not the property anyone cares
// about; "a new person can write their first issue and see it" is. So it bootstraps, posts a title,
// and reads the list back.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workflow"
	"github.com/talyvor/track/internal/workspace"
	"github.com/talyvor/track/migrations"
)

// ownerMemberID reads the member row CreateWithOwner seeds, which is what the authz middleware
// would resolve for this identity. Taking it from the database rather than inventing one keeps the
// test on the same identity the production path uses.
func ownerMemberID(t *testing.T, d *testutil.DB, wsID string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT id FROM members WHERE workspace_id = $1 AND role = 'owner'`, wsID).Scan(&id); err != nil {
		t.Fatalf("owner member for %s: %v", wsID, err)
	}
	return id
}

// postIssue drives the REAL issue handler over HTTP with the context the authz middleware leaves
// behind, carrying exactly the body the BFF forwards from the browser: a title, and nothing else.
func postIssue(t *testing.T, h *issue.Handler, wsID, memberID, body string) (int, string) {
	t.Helper()
	r := chi.NewRouter()
	h.Mount(r)

	req := httptest.NewRequest(http.MethodPost, "/workspaces/"+wsID+"/issues", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(authz.WithAuthorized(req.Context(), wsID, memberID))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

// TestBrandNewIdentityCanCreateItsFirstIssue is the user's own sequence: sign in, open Track, create
// an issue, see it appear.
func TestBrandNewIdentityCanCreateItsFirstIssue(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	code, out := bootstrapOnce(t, h, "https://accounts.google.com", "sub-first-issue", "new@example.com")
	if code != http.StatusOK {
		t.Fatalf("bootstrap: got %d", code)
	}
	if !out.Created {
		t.Fatal("bootstrap did not create a workspace — this identity must be brand new")
	}

	issueH := issue.NewHandler(issue.NewStore(d.Pool))
	member := ownerMemberID(t, d, out.WorkspaceID)

	// ⚠ THE BODY IS EXACTLY WHAT THE BROWSER SENDS. apps/web/src/areas/track/IssueList.tsx posts
	// JSON.stringify({ title }), and the BFF forwards it verbatim. Adding a team_id here would test
	// a request nothing makes.
	code, body := postIssue(t, issueH, out.WorkspaceID, member, `{"title":"Write the thing down"}`)
	if code != http.StatusCreated {
		t.Fatalf("create issue: got %d (%s), want 201 — a new workspace must be able to take an issue", code, body)
	}

	var created model.Issue
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("create issue: unreadable response %q: %v", body, err)
	}
	if created.TeamID == "" {
		t.Fatal("the created issue has no team — issues.team_id is NOT NULL, so this cannot have been stored")
	}
	// The team is not decoration: it is the NAMESPACE of the issue's human-facing key. The store
	// reads teams.identifier and builds "<identifier>-<number>", so an issue with no team has no
	// name to refer to it by.
	if created.Identifier == "" || !strings.Contains(created.Identifier, "-") {
		t.Fatalf("issue identifier = %q, want the <TEAM>-<n> key the store derives from the team", created.Identifier)
	}

	// AND IT APPEARS. A create that stores a row the list cannot return is the same dead end wearing
	// a 201.
	listReq := httptest.NewRequest(http.MethodGet, "/workspaces/"+out.WorkspaceID+"/issues", nil)
	listReq = listReq.WithContext(authz.WithAuthorized(listReq.Context(), out.WorkspaceID, member))
	listRR := httptest.NewRecorder()
	r := chi.NewRouter()
	issueH.Mount(r)
	r.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list issues: got %d (%s)", listRR.Code, listRR.Body.String())
	}
	if !strings.Contains(listRR.Body.String(), "Write the thing down") {
		t.Fatalf("the created issue is not in the list: %s", listRR.Body.String())
	}
}

// TestEveryWorkspaceCreationPathSeedsATeam.
//
// ⚠ FIXING BOOTSTRAP ALONE WOULD HAVE HIDDEN THE FALLBACK. Two routes create workspaces and BOTH
// call CreateWithOwner: POST /v1/bootstrap (login) and POST /v1/workspaces (user-driven, "make me a
// second workspace"). A default team seeded in the bootstrap HANDLER would leave every
// hand-created workspace teamless — the same bug, reachable by a different click. The seed belongs
// in the shared chokepoint, and this pins that it is there rather than in one caller.
func TestEveryWorkspaceCreationPathSeedsATeam(t *testing.T) {
	d := testutil.New(t)
	store := workspace.NewStore(d.Pool)
	ctx := context.Background()

	code, viaBootstrap := bootstrapOnce(t, workspace.NewHandler(store),
		"https://accounts.google.com", "sub-paths", "paths@example.com")
	if code != http.StatusOK {
		t.Fatalf("bootstrap: got %d", code)
	}

	direct, err := store.CreateWithOwner(ctx,
		model.Workspace{Name: "Platform", Slug: "platform-" + t.Name()}, "paths@example.com")
	if err != nil {
		t.Fatalf("CreateWithOwner: %v", err)
	}

	// BOTH paths, or the test's name is a claim it does not check.
	for _, tc := range []struct{ path, wsID string }{
		{"POST /v1/bootstrap", viaBootstrap.WorkspaceID},
		{"POST /v1/workspaces", direct.ID},
	} {
		wsID := tc.wsID
		var n int
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM teams WHERE workspace_id = $1`, wsID).Scan(&n); err != nil {
			t.Fatalf("count teams: %v", err)
		}
		if n == 0 {
			t.Errorf("%s: workspace %s has NO team — it cannot take a single issue", tc.path, wsID)
		}
	}
}

// TestDefaultTeamIsNotASecondClassTeam: a team created by the workspace path must look like a team
// created through POST /v1/workspaces/{id}/teams, which seeds the six built-in workflow statuses.
// Two ways to make a team that produce different teams is the second code path this repo already
// refuses elsewhere.
func TestDefaultTeamIsNotASecondClassTeam(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	// Wired exactly as cmd/track/main.go wires it — an unwired store would prove nothing about the
	// binary that ships.
	store := workspace.NewStore(d.Pool).WithWorkflowSeeder(workflow.New(d.Pool))
	code, out := bootstrapOnce(t, workspace.NewHandler(store),
		"https://accounts.google.com", "sub-workflow", "workflow@example.com")
	if code != http.StatusOK {
		t.Fatalf("bootstrap: got %d", code)
	}

	var statuses int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM workflow_statuses ws
		   JOIN teams t ON t.id = ws.team_id
		  WHERE t.workspace_id = $1`, out.WorkspaceID).Scan(&statuses); err != nil {
		t.Fatalf("count workflow statuses: %v", err)
	}
	if statuses == 0 {
		t.Error("the default team has no workflow statuses — a hand-created team gets six")
	}
}

// TestBackfillRepairsAWorkspaceThatAlreadyExists.
//
// ⚠ FIXING CREATION ALONE WOULD HAVE LEFT THE REPORTER BROKEN. CreateWithOwner now seeds a team, so
// every workspace made from here on is fine — and every workspace that already exists still has
// none. Those are exactly the workspaces belonging to the people who hit this, including the one the
// bug was reported from. So migration 0025 backfills them, and this drives the SHIPPED SQL (read
// from the embedded FS, not a copy retyped into the test) against a workspace put deliberately into
// the pre-release state.
func TestBackfillRepairsAWorkspaceThatAlreadyExists(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	code, out := bootstrapOnce(t, workspace.NewHandler(workspace.NewStore(d.Pool)),
		"https://accounts.google.com", "sub-backfill", "backfill@example.com")
	if code != http.StatusOK {
		t.Fatalf("bootstrap: got %d", code)
	}

	// Model a workspace created BEFORE this release: it has an owner and no team.
	if _, err := d.Pool.Exec(ctx, `DELETE FROM workflow_statuses WHERE team_id IN (SELECT id FROM teams WHERE workspace_id = $1)`, out.WorkspaceID); err != nil {
		t.Fatalf("clear workflow statuses: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, `DELETE FROM teams WHERE workspace_id = $1`, out.WorkspaceID); err != nil {
		t.Fatalf("clear teams: %v", err)
	}

	// RED CONTROL: prove the pre-state really is broken, so a backfill that did nothing could not
	// pass this test.
	issueH := issue.NewHandler(issue.NewStore(d.Pool))
	member := ownerMemberID(t, d, out.WorkspaceID)
	if code, body := postIssue(t, issueH, out.WorkspaceID, member, `{"title":"before"}`); code == http.StatusCreated {
		t.Fatalf("a teamless workspace accepted an issue (%s) — the pre-state is not the broken one", body)
	}

	sqlBytes, err := migrations.FS.ReadFile("0025_default_team_backfill.sql")
	if err != nil {
		t.Fatalf("read the shipped migration: %v", err)
	}
	if _, err := d.Pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("apply 0025: %v", err)
	}

	// GREEN: the same request now works, which is the only property that matters.
	if code, body := postIssue(t, issueH, out.WorkspaceID, member, `{"title":"after"}`); code != http.StatusCreated {
		t.Fatalf("after backfill: got %d (%s), want 201", code, body)
	}

	var statuses int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM workflow_statuses ws JOIN teams t ON t.id = ws.team_id WHERE t.workspace_id = $1`,
		out.WorkspaceID).Scan(&statuses); err != nil {
		t.Fatalf("count statuses: %v", err)
	}
	if statuses != 6 {
		t.Errorf("backfilled team has %d workflow statuses, want the same 6 a hand-created team gets", statuses)
	}

	// IDEMPOTENT: migrations get re-run, and a second apply must not add a second team.
	if _, err := d.Pool.Exec(ctx, string(sqlBytes)); err != nil {
		t.Fatalf("re-apply 0025: %v", err)
	}
	var teams int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM teams WHERE workspace_id = $1`, out.WorkspaceID).Scan(&teams); err != nil {
		t.Fatalf("count teams: %v", err)
	}
	if teams != 1 {
		t.Errorf("after re-applying the backfill the workspace has %d teams, want 1", teams)
	}
}
