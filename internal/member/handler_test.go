package member_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/member"
	"github.com/talyvor/track/internal/testutil"
)

const testSyncSecret = "test-member-sync-secret-0123456789"

func seedMember(t *testing.T, d *testutil.DB, wsID, email, role string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1, $2, $3, $4)`,
		wsID, email, email, role); err != nil {
		t.Fatalf("seed member: %v", err)
	}
}

// memberChain mounts the service handler exactly as main.go will (inside /v1); the
// handler does its OWN bearer auth (the route is gwExempt), so no gateway middleware.
func memberChain(secret string, d *testutil.DB) http.Handler {
	h := member.NewHandler(member.NewStore(d.Pool), secret)
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) { h.Mount(r) })
	return r
}

func getReq(wsID, bearer string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/service/members?workspace_id="+wsID, nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

// (a) AUTH — no token, wrong token, and unset-secret all → 401.
func TestServiceMembers_Auth(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	h := memberChain(testSyncSecret, d)

	for _, tc := range []struct {
		name, bearer string
	}{{"no token", ""}, {"wrong token", "wrong-token"}} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, getReq(ws.ID, tc.bearer))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s = %d, want 401", tc.name, rr.Code)
		}
	}

	// unset secret → refuses ALL requests, even a "matching" empty token.
	hEmpty := memberChain("", d)
	for _, bearer := range []string{"", "anything", ""} {
		rr := httptest.NewRecorder()
		hEmpty.ServeHTTP(rr, getReq(ws.ID, bearer))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("unset secret (bearer=%q) = %d, want 401 (refuses all)", bearer, rr.Code)
		}
	}
}

// (b) SCOPING + (c) PROJECTION — A's roster only, projected to email/role/member_id.
func TestServiceMembers_ScopedAndProjected(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	seedMember(t, d, wsA.ID, "alice@corp.com", "admin")
	seedMember(t, d, wsA.ID, "carol@corp.com", "member")
	seedMember(t, d, wsB.ID, "bob@corp.com", "member") // the cross-tenant leak canary

	rr := httptest.NewRecorder()
	memberChain(testSyncSecret, d).ServeHTTP(rr, getReq(wsA.ID, testSyncSecret))
	if rr.Code != http.StatusOK {
		t.Fatalf("valid request = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var got []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	emails := map[string]bool{}
	for _, m := range got {
		emails[m["email"].(string)] = true
	}
	if !emails["alice@corp.com"] || !emails["carol@corp.com"] {
		t.Fatalf("workspace A roster incomplete: %v", emails)
	}
	if emails["bob@corp.com"] {
		t.Fatal("CROSS-WORKSPACE LEAK: bob@ (workspace B) returned for a workspace-A query")
	}
	if len(got) != 2 {
		t.Fatalf("got %d members, want exactly 2 (alice, carol)", len(got))
	}
	// (c) projection: ONLY email/role/member_id — never name/avatar/created_at.
	for _, m := range got {
		for k := range m {
			if k != "email" && k != "role" && k != "member_id" {
				t.Fatalf("over-return: key %q present (want only email/role/member_id)", k)
			}
		}
	}
}

// no workspace_id → 400 (never a full-table dump of every tenant's members).
func TestServiceMembers_NoWorkspaceID_400(t *testing.T) {
	d := testutil.New(t)
	seedMember(t, d, d.Workspace(t).ID, "x@corp.com", "member")

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/service/members", nil) // no workspace_id
	r.Header.Set("Authorization", "Bearer "+testSyncSecret)
	memberChain(testSyncSecret, d).ServeHTTP(rr, r)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing workspace_id = %d, want 400 (must never dump all members)", rr.Code)
	}
}
