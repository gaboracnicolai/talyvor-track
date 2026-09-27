package lensintegration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// asAdminLens answers Lens's GET /v1/auth/me as an ADMIN key (every workspace served) and hands every
// other request to h, so the fake Lens servers in this package keep testing what they always tested.
func asAdminLens(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/me" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"workspace_id":"","is_admin":true}`))
			return
		}
		h(w, r)
	})
}

// workspaceKeyLens is a fake Lens behind a WORKSPACE key for ws-K: like the real one, it ignores
// ?workspace_id= and always serves ws-K's rows, and it records which workspaces Track asked for.
func workspaceKeyLens(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	asked := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/auth/me" {
			_, _ = w.Write([]byte(`{"workspace_id":"ws-K","is_admin":false}`))
			return
		}
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("workspace_id"))
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/api/spend/by-request":
			_, _ = w.Write([]byte(byRequestBody(`[{"request_id":"req-K1","feature":"ENG-1","cost_usd":2.5}]`)))
		default:
			_, _ = w.Write([]byte(`{"total_cost_usd":99}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// Under a workspace key the syncer reads only the key's own workspace; another Track workspace is
// never asked for and never credited with the key's workspace's spend.
func TestSyncer_WorkspaceKeySyncsOnlyItsOwnWorkspace(t *testing.T) {
	srv, asked := workspaceKeyLens(t)
	up := &fakeUpdater{}
	s := NewSyncer(New(srv.URL, "lens_sk_ws_K"), up, &fakeWorkspaces{ids: []string{"ws-A", "ws-K"}})

	s.runOnce(context.Background())

	for _, ws := range *asked {
		if ws != "ws-K" {
			t.Fatalf("Track asked Lens for workspace %q under ws-K's key; want only ws-K (asked: %v)", ws, *asked)
		}
	}
	if len(up.calls) != 1 || up.calls[0].Workspace != "ws-K" {
		t.Fatalf("spend recorded = %+v, want exactly one row, recorded against ws-K", up.calls)
	}
}

// The Lens panel of another workspace gets nothing rather than ws-K's figures.
func TestClient_WorkspaceKeyRefusesAnotherWorkspace(t *testing.T) {
	srv, asked := workspaceKeyLens(t)
	c := New(srv.URL, "lens_sk_ws_K")

	if _, err := c.GetSpendSummary(context.Background(), "ws-A", 30); !errors.Is(err, ErrWorkspaceNotServed) {
		t.Fatalf("GetSpendSummary(ws-A) under ws-K's key = %v, want ErrWorkspaceNotServed", err)
	}
	if len(*asked) != 0 {
		t.Fatalf("Lens was asked for %v; a refused workspace must not be requested at all", *asked)
	}
	if s, err := c.GetSpendSummary(context.Background(), "ws-K", 30); err != nil || s.TotalCostUSD != 99 {
		t.Fatalf("GetSpendSummary(ws-K) = %+v, %v; want ws-K's own figures", s, err)
	}
}
