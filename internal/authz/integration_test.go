package authz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/workspace"
	"github.com/talyvor/track/internal/testutil"
)

const secret = "test-gateway-transit-secret-0123456789"

func seedMember(t *testing.T, d *testutil.DB, wsID, email, role string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1, $2, $3, $4) RETURNING id`,
		wsID, email, email, role).Scan(&id); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return id
}

var noExempt = func(string) bool { return false }

// fullChain wires the REAL T9 (transit proof) + T10 (membership authz) middleware on /v1.
func fullChain(d *testutil.DB, mount func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		mount(r)
	})
	return r
}

// t9OnlyChain is the PRE-T10 state: transit proof verified, but NO membership authz — the
// live IDOR hole.
func t9OnlyChain(d *testutil.DB, mount func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, noExempt))
		mount(r)
	})
	return r
}

func reqAs(method, path, email string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set(gatewayauth.HeaderGatewayAuth, secret)
	r.Header.Set(gatewayauth.HeaderUserEmail, email)
	return r
}

// oldStyleProbe is the PRE-T10 handler: it scopes by the URL {wsID} (chi.URLParam in the
// handler, where the param IS populated) — the behavior T10 replaces. Used to show the
// IDOR was live before T10.
func oldStyleProbe(reached *string) func(chi.Router) {
	return func(r chi.Router) {
		r.Get("/workspaces/{wsID}/probe", func(w http.ResponseWriter, r *http.Request) {
			*reached = chi.URLParam(r, "wsID")
			w.WriteHeader(http.StatusOK)
		})
	}
}

// wsProbe records the SERVER-RESOLVED workspace it was allowed to act on (or 403s if none
// is authorized) — so a test can prove both the deny and that the scope SOURCE is the
// authorized value, not the URL.
func wsProbe(reached *string) func(chi.Router) {
	return func(r chi.Router) {
		r.Get("/workspaces/{wsID}/probe", func(w http.ResponseWriter, r *http.Request) {
			ws, ok := authz.WorkspaceID(r.Context())
			if !ok {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			*reached = ws
			w.WriteHeader(http.StatusOK)
		})
	}
}

// TestIDOR_MemberOfA_CannotReachB — THE core proof. With T10 a member of A acting on B is
// 403 and never reaches B; without T10 (T9 only) the same caller reaches B (the live
// hole). And legitimate access to A still works.
func TestIDOR_MemberOfA_CannotReachB(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	seedMember(t, d, wsA.ID, "x@corp.com", "member") // X is a member of A only

	// RED — pre-T10 (T9 only; the handler scopes by the URL param as before T10): X
	// reaches B's data.
	var redReached string
	red := t9OnlyChain(d, oldStyleProbe(&redReached))
	rr := httptest.NewRecorder()
	red.ServeHTTP(rr, reqAs("GET", "/v1/workspaces/"+wsB.ID+"/probe", "x@corp.com"))
	if rr.Code != http.StatusOK || redReached != wsB.ID {
		t.Fatalf("RED expected the IDOR live: code=%d reached=%q, want 200 / %s", rr.Code, redReached, wsB.ID)
	}

	// GREEN — with T10: X on B → 403, B never reached.
	var reached string
	green := fullChain(d, wsProbe(&reached))
	reached = ""
	rrB := httptest.NewRecorder()
	green.ServeHTTP(rrB, reqAs("GET", "/v1/workspaces/"+wsB.ID+"/probe", "x@corp.com"))
	if rrB.Code != http.StatusForbidden {
		t.Fatalf("member-of-A on wsID=B = %d, want 403 (IDOR cure)", rrB.Code)
	}
	if reached != "" {
		t.Fatalf("handler reached workspace %q for a non-member — B was accessible", reached)
	}

	// GREEN — legitimate: X on A → 200, scope source = A (from membership).
	reached = ""
	rrA := httptest.NewRecorder()
	green.ServeHTTP(rrA, reqAs("GET", "/v1/workspaces/"+wsA.ID+"/probe", "x@corp.com"))
	if rrA.Code != http.StatusOK || reached != wsA.ID {
		t.Fatalf("member-of-A on wsID=A = %d reached=%q, want 200 / %s", rrA.Code, reached, wsA.ID)
	}
}

// TestNoMembership_Denied — a verified user with no membership row → 403 on any wsID (a
// clean deny, not a crash).
func TestNoMembership_Denied(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	seedMember(t, d, wsA.ID, "x@corp.com", "member")

	var reached string
	h := fullChain(d, wsProbe(&reached))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, reqAs("GET", "/v1/workspaces/"+wsA.ID+"/probe", "nobody@corp.com"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("no-membership user = %d, want 403", rr.Code)
	}
}

// TestActor_ResolvedMemberID_NotSpoofedHeader — the actor is the resolved member.id, and a
// forged X-Member-Id is ignored.
func TestActor_ResolvedMemberID_NotSpoofedHeader(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	xMemberID := seedMember(t, d, wsA.ID, "x@corp.com", "member")

	var gotActor string
	h := fullChain(d, func(r chi.Router) {
		r.Get("/workspaces/{wsID}/whoami", func(w http.ResponseWriter, r *http.Request) {
			mid, ok := authz.MemberID(r.Context())
			if !ok {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			gotActor = mid
			w.WriteHeader(http.StatusOK)
		})
	})
	req := reqAs("GET", "/v1/workspaces/"+wsA.ID+"/whoami", "x@corp.com")
	req.Header.Set("X-Member-Id", "forged-member-id-attacker") // spoof attempt
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("whoami = %d, want 200", rr.Code)
	}
	if gotActor != xMemberID {
		t.Fatalf("actor = %q, want the RESOLVED member.id %q (forged X-Member-Id must be ignored)", gotActor, xMemberID)
	}
	if gotActor == "forged-member-id-attacker" {
		t.Fatal("the forged X-Member-Id was used as the actor")
	}
}

// TestCreate_ReachableByCreator — POST /v1/workspaces by a brand-new user (no membership)
// creates the workspace + seeds them owner atomically, so a scoped request to it
// immediately succeeds.
func TestCreate_ReachableByCreator(t *testing.T) {
	d := testutil.New(t)
	wsStore := workspace.NewStore(d.Pool)
	wsHandler := workspace.NewHandler(wsStore)

	var reached string
	h := fullChain(d, func(r chi.Router) {
		wsHandler.Mount(r)
		wsProbe(&reached)(r)
	})

	// create as a new user
	body := strings.NewReader(`{"name":"Acme","slug":"acme"}`)
	req := httptest.NewRequest("POST", "/v1/workspaces", body)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, "founder@acme.com")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	// resolve the new workspace id
	var newWS string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT id FROM workspaces WHERE slug='acme'`).Scan(&newWS); err != nil {
		t.Fatal(err)
	}
	// and a member row exists with role owner
	var role string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT role FROM members WHERE workspace_id=$1 AND email='founder@acme.com'`, newWS).Scan(&role); err != nil {
		t.Fatalf("owner member not seeded: %v", err)
	}
	if role != "owner" {
		t.Errorf("creator role = %q, want owner", role)
	}

	// immediately reachable by the creator
	reached = ""
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, reqAs("GET", "/v1/workspaces/"+newWS+"/probe", "founder@acme.com"))
	if rr2.Code != http.StatusOK || reached != newWS {
		t.Fatalf("created workspace not reachable by creator: code=%d reached=%q", rr2.Code, reached)
	}
}

// TestNoWsID_FailsClosed — a /v1 route WITHOUT {wsID} that reads WorkspaceID gets ok=false
// (and a "" id), never a match-all empty string.
func TestNoWsID_FailsClosed(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	seedMember(t, d, wsA.ID, "x@corp.com", "member")

	var sawOK bool
	var sawID string
	h := fullChain(d, func(r chi.Router) {
		r.Get("/no-ws-probe", func(w http.ResponseWriter, r *http.Request) {
			sawID, sawOK = authz.WorkspaceID(r.Context())
			w.WriteHeader(http.StatusOK)
		})
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, reqAs("GET", "/v1/no-ws-probe", "x@corp.com"))
	if sawOK {
		t.Fatal("WorkspaceID returned ok=true on a no-{wsID} route — fail-open")
	}
	if sawID != "" {
		t.Fatalf("WorkspaceID returned %q on a no-{wsID} route — a store could treat it as match-all", sawID)
	}
}
