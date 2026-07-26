package member

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// workspaces_test.go — GET /v1/service/workspaces, the endpoint that breaks the Docs cold-start
// deadlock.
//
// THE DEADLOCK. Docs enumerated the workspaces it should sync rosters for from its OWN content:
// `SELECT workspace_id FROM spaces UNION SELECT workspace_id FROM pages`
// (docs internal/membership/store.go). A workspace with no content is therefore never enumerated,
// never gets a roster synced, and every write into it 403s for want of a membership row — so the
// first page can never be created, so there is never any content. A brand-new tenant is locked out
// of Docs permanently, and Track is now the tenancy source of truth that actually knows the
// workspace exists.
//
// ⚠ THIS ENDPOINT IS A FULL-DEPLOYMENT DUMP, which the sibling /service/members deliberately is
// not — that one refuses to run without an explicit workspace_id precisely to avoid a mass read.
// The difference is the point of the endpoint: enumeration IS the request. So the compensating
// controls have to be stated and tested rather than assumed:
//
//   · service-only. Same gwExempt /v1/service/ path and the same MemberSyncSecret bearer as
//     /service/members. A tenant credential cannot reach it, because tenant credentials do not
//     travel on this path at all.
//   · fail-closed on an unset secret. secret=="" ⇒ every request 401s, so a deployment that has
//     not configured member-sync never serves the list of every workspace it hosts.
//   · IDs ONLY. No names, no member counts, no content. The response is the minimum Docs needs to
//     ask its next question, so a leaked response is a list of opaque slugs.

func newTestHandler(t *testing.T, secret string, ids []string) http.Handler {
	t.Helper()
	h := &WorkspacesHandler{lister: stubLister{ids: ids}, secret: secret}
	r := chi.NewRouter()
	r.Route("/v1", func(v1 chi.Router) { h.Mount(v1) })
	return r
}

type stubLister struct {
	ids  []string
	err  error
	seen int
}

func (s stubLister) ListWorkspaceIDs(ctx context.Context) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.ids, nil
}

func get(t *testing.T, h http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/service/workspaces", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The endpoint exists and returns the deployment's workspace ids to a correct service token.
func TestServiceWorkspaces_ListsForAValidToken(t *testing.T) {
	h := newTestHandler(t, "a-service-secret-long-enough-for-the-guard", []string{"ws-a", "ws-b"})
	rec := get(t, h, "a-service-secret-long-enough-for-the-guard")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"ws-a", "ws-b"} {
		if !contains(body, want) {
			t.Errorf("response does not list %q: %s", want, body)
		}
	}
}

// ⚠ The control that matters most: a wrong, absent or partial token gets nothing. This endpoint
// enumerates EVERY workspace on the deployment, so an auth miss here is a disclosure of the whole
// tenant list.
func TestServiceWorkspaces_RefusesWithoutTheServiceToken(t *testing.T) {
	const secret = "a-service-secret-long-enough-for-the-guard"
	for _, c := range []struct{ name, tok string }{
		{"absent", ""},
		{"wrong", "not-the-secret"},
		{"prefix of the real secret", secret[:10]},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := get(t, newTestHandler(t, secret, []string{"ws-a"}), c.tok)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if contains(rec.Body.String(), "ws-a") {
				t.Errorf("a refused request still disclosed a workspace id: %s", rec.Body.String())
			}
		})
	}
}

// An unset secret must not mean an open endpoint. This mirrors /service/members: member-sync
// unconfigured ⇒ the highest-value read is never served open.
func TestServiceWorkspaces_UnsetSecretFailsClosed(t *testing.T) {
	rec := get(t, newTestHandler(t, "", []string{"ws-a"}), "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — an unset secret must refuse every request, not admit them", rec.Code)
	}
	// And an attacker who guesses the empty string must not be admitted by a comparison that
	// treats "" == "" as a match.
	rec2 := get(t, newTestHandler(t, "", []string{"ws-a"}), "")
	if rec2.Code == http.StatusOK {
		t.Errorf("empty configured secret admitted a request")
	}
}

// The response carries IDs and nothing else. A leaked response should be a list of opaque slugs,
// not a map of the deployment.
func TestServiceWorkspaces_ReturnsIdsOnly(t *testing.T) {
	h := newTestHandler(t, "a-service-secret-long-enough-for-the-guard", []string{"ws-a"})
	body := get(t, h, "a-service-secret-long-enough-for-the-guard").Body.String()
	for _, leak := range []string{"name", "member", "email", "count", "owner"} {
		if contains(body, leak) {
			t.Errorf("response includes %q — this endpoint must return ids only: %s", leak, body)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
