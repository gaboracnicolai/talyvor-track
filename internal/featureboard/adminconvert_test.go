package featureboard_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/featureboard"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// convertEnv seeds a workspace, team, board, and post, and returns the DB (so a test can
// assert on the persisted row, not just the response) plus a router with the convert
// route mounted and the path-relevant IDs.
func convertEnv(t *testing.T) (d *testutil.DB, router http.Handler, wsID, boardID, postID, teamID string) {
	t.Helper()
	d = testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	fb := featureboard.NewStore(d.Pool)
	board, err := fb.CreateBoard(context.Background(), featureboard.Board{WorkspaceID: ws.ID, Name: "Roadmap"})
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	post, err := fb.CreatePost(context.Background(), featureboard.FeaturePost{
		WorkspaceID: ws.ID, BoardID: board.ID, Title: "Dark mode", AuthorName: "Ann",
	})
	if err != nil {
		t.Fatalf("CreatePost: %v", err)
	}
	h := featureboard.NewHandler(fb, issue.NewStore(d.Pool))
	r := chi.NewRouter()
	r.Post("/workspaces/{wsID}/boards/{boardID}/posts/{postID}/convert", h.AdminConvert)
	return d, r, ws.ID, board.ID, post.ID, team.ID
}

// issueCreatorFromDB reads the persisted creator_id of a converted issue. Asserting on
// the ROW rather than the response is what makes the authorship tests load-bearing.
func issueCreatorFromDB(t *testing.T, d *testutil.DB, issueID string) string {
	t.Helper()
	var creator string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT creator_id FROM issues WHERE id=$1`, issueID).Scan(&creator); err != nil {
		t.Fatalf("read creator_id for issue %s: %v", issueID, err)
	}
	return creator
}

func convert(t *testing.T, r http.Handler, wsID, boardID, postID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/workspaces/"+wsID+"/boards/"+boardID+"/posts/"+postID+"/convert",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	// T10: AdminConvert now reads the authorized workspace from context (set by the
	// authz middleware in production). Inject the path workspace as the authorized
	// one so these tests exercise the handler body rather than a 403.
	req = req.WithContext(authz.WithAuthorized(req.Context(), wsID, "admin-1"))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// TestAdminConvert_MissingTeamID_ClearError — team_id is a genuine client-supplied
// parameter (the admin chooses which team receives the issue), so omitting it is still a
// clear contract error rather than a cryptic CONVERT_FAILED from issues.Create.
//
// NOTE: this test previously asserted the SAME contract for creator_id. That assertion
// was pinning the forged-authorship defect — it required the client to name the actor,
// which is precisely what let a caller name someone else. Requiring an identity the
// server already knows is never a contract; it is an injection point. Authorship is now
// covered by convert_authorship_test.go.
func TestAdminConvert_MissingTeamID_ClearError(t *testing.T) {
	_, r, wsID, boardID, postID, _ := convertEnv(t)
	rr := convert(t, r, wsID, boardID, postID, `{}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "team_id required") {
		t.Fatalf("body = %s, want a clear 'team_id required' message", rr.Body.String())
	}
}

// TestAdminConvert_Valid_CreatesIssue — with team_id the post converts to an issue (201).
func TestAdminConvert_Valid_CreatesIssue(t *testing.T) {
	_, r, wsID, boardID, postID, teamID := convertEnv(t)
	rr := convert(t, r, wsID, boardID, postID, `{"team_id":"`+teamID+`"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "issue_id") {
		t.Errorf("response missing issue_id: %s", rr.Body.String())
	}
}
