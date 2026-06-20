package featureboard_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/featureboard"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// convertEnv seeds a workspace, team, board, and post, and returns a router with the
// convert route mounted plus the path-relevant IDs.
func convertEnv(t *testing.T) (router http.Handler, wsID, boardID, postID, teamID string) {
	t.Helper()
	d := testutil.New(t)
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
	return r, ws.ID, board.ID, post.ID, team.ID
}

func convert(t *testing.T, r http.Handler, wsID, boardID, postID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/workspaces/"+wsID+"/boards/"+boardID+"/posts/"+postID+"/convert",
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// TestAdminConvert_MissingCreatorID_ClearError — omitting creator_id yields a clear
// contract error, not the cryptic CONVERT_FAILED DB failure from issues.Create.
func TestAdminConvert_MissingCreatorID_ClearError(t *testing.T) {
	r, wsID, boardID, postID, teamID := convertEnv(t)
	rr := convert(t, r, wsID, boardID, postID, `{"team_id":"`+teamID+`"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "creator_id required") {
		t.Fatalf("body = %s, want a clear 'creator_id required' message", rr.Body.String())
	}
}

// TestAdminConvert_Valid_CreatesIssue — with team_id + creator_id the post converts to
// an issue (201).
func TestAdminConvert_Valid_CreatesIssue(t *testing.T) {
	r, wsID, boardID, postID, teamID := convertEnv(t)
	rr := convert(t, r, wsID, boardID, postID, `{"team_id":"`+teamID+`","creator_id":"admin-1"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "issue_id") {
		t.Errorf("response missing issue_id: %s", rr.Body.String())
	}
}
