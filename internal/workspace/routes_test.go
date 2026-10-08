package workspace_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/timetracking"
	"github.com/talyvor/track/internal/workspace"
)

// B17.109: Delete workspace and Restore are reached through the /v1 router as cmd/track mounts it —
// the workspace routes beside time tracking's /workspaces/{wsID}/... — not only by calling the handler.
// Time tracking used to mount a sub-router on /workspaces/{wsID}, which took every method on that
// path, and both answered a plain 404 in production.
func TestWorkspace_DeleteAndRestoreThroughTheV1Router(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	const owner = "owner-b17109@example.com"
	ws, err := workspace.NewStore(d.Pool).CreateWithOwner(ctx, model.Workspace{Name: "Kept", Slug: "kept-b17109"}, owner)
	if err != nil {
		t.Fatalf("CreateWithOwner: %v", err)
	}
	iss := d.Issue(t, ws.ID, d.Team(t, ws.ID).ID)

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				next.ServeHTTP(w, q.WithContext(gatewayauth.WithIdentity(q.Context(), gatewayauth.Identity{Email: owner})))
			})
		})
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), nil))
		workspace.NewHandler(workspace.NewStore(d.Pool)).Mount(r)
		timetracking.NewHandler(timetracking.NewStore(d.Pool)).Mount(r)
	})
	send := func(method, path, body string) *httptest.ResponseRecorder {
		var q *http.Request
		if body != "" {
			q = httptest.NewRequest(method, path, strings.NewReader(body))
			q.Header.Set("Content-Type", "application/json")
		} else {
			q = httptest.NewRequest(method, path, nil)
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, q)
		return rr
	}

	if rr := send(http.MethodDelete, "/v1/workspaces/"+ws.ID, `{"confirm":"kept-b17109"}`); rr.Code != http.StatusOK {
		t.Fatalf("DELETE /v1/workspaces/{id} = %d %q, want 200", rr.Code, rr.Body.String())
	}
	if !wsDeleted(t, d, ws.ID) {
		t.Fatal("the delete answered 200 and the workspace is not deleted")
	}
	if rr := send(http.MethodGet, "/v1/workspaces?deleted=true", ""); !strings.Contains(rr.Body.String(), ws.ID) {
		t.Errorf("the owner's deleted workspaces do not hold it: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(http.MethodGet, "/v1/workspaces", ""); strings.Contains(rr.Body.String(), ws.ID) {
		t.Errorf("a deleted workspace is still listed live: %s", rr.Body.String())
	}

	if rr := send(http.MethodPost, "/v1/workspaces/"+ws.ID+"/restore", ""); rr.Code != http.StatusOK {
		t.Fatalf("POST /v1/workspaces/{id}/restore = %d %q, want 200", rr.Code, rr.Body.String())
	}
	if rr := send(http.MethodGet, "/v1/workspaces", ""); !strings.Contains(rr.Body.String(), ws.ID) {
		t.Errorf("the restored workspace is not listed live: %s", rr.Body.String())
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM issues WHERE id=$1 AND workspace_id=$2`, iss.ID, ws.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("the restored workspace's issue is missing: n=%d err=%v", n, err)
	}
	if rr := send(http.MethodGet, "/v1/workspaces/"+ws.ID+"/time-summary", ""); rr.Code != http.StatusOK {
		t.Errorf("time tracking's /workspaces/{id}/time-summary = %d %q, want 200", rr.Code, rr.Body.String())
	}
}
