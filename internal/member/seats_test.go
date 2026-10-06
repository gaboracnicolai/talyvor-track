package member_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/lenscreds"
	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/member"
	"github.com/talyvor/track/internal/testutil"
)

const testMintKey = "test-mint-key"

// lensSeatsStub is Lens as Track meets it: POST /v1/auth/token mints a token for one workspace with the mint key,
// and GET /v1/workspaces/{ws}/plan/seats?members=N (B32.12) admits only a token minted for {ws} — Lens's
// workspaceIsolationMiddleware — then answers with Lens's own shapes and wording for the plan that workspace is
// on: free takes 1 seat, team 5. asked records every workspace a seats check named.
type lensSeatsStub struct {
	*httptest.Server
	mu    sync.Mutex
	asked []string
}

func newLensSeatsStub(t *testing.T, planOf map[string]string) *lensSeatsStub {
	t.Helper()
	stub := &lensSeatsStub{}
	seats := map[string]int64{"free": 1, "team": 5}
	next := map[string]string{"free": "team", "team": "business"}
	nextSeats := map[string]int64{"team": 5, "business": 25}
	r := chi.NewRouter()
	r.Post("/v1/auth/token", func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			WorkspaceID string `json:"workspace_id"`
		}
		if req.Header.Get("Authorization") != "Bearer "+testMintKey || json.NewDecoder(req.Body).Decode(&in) != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "jwt-for-" + in.WorkspaceID,
			"expires_at": time.Now().Add(12 * time.Hour)})
	})
	r.Get("/v1/workspaces/{ws}/plan/seats", func(w http.ResponseWriter, req *http.Request) {
		ws := chi.URLParam(req, "ws")
		stub.mu.Lock()
		stub.asked = append(stub.asked, ws)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Header.Get("Authorization") != "Bearer jwt-for-"+ws {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden: credential not authorized for this workspace"}`))
			return
		}
		plan := planOf[ws]
		n, _ := strconv.ParseInt(req.URL.Query().Get("members"), 10, 64)
		if n <= seats[plan] {
			_ = json.NewEncoder(w).Encode(map[string]any{"plan": plan, "seats": seats[plan], "members": n})
			return
		}
		limit, allows := seats[plan], next[plan]
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": fmt.Sprintf("LENS_PLAN_GATES: the %s plan allows %d %s — the %s plan allows %d seats",
				plan, limit, map[bool]string{true: "seat", false: "seats"}[limit == 1], allows, nextSeats[allows]),
			"plan": plan, "gate": "seats", "limit": limit, "allows": allows})
	})
	stub.Server = httptest.NewServer(r)
	t.Cleanup(stub.Close)
	return stub
}

func (s *lensSeatsStub) askedAbout() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

// seatsChain mounts the member API as main.go does with Lens deployed and TRACK_LENS_MINT_KEY set.
func seatsChain(d *testutil.DB, lensURL string) http.Handler {
	exempt := func(string) bool { return false }
	lens := lensintegration.New(lensURL, "workspace-read-key")
	h := member.NewMgmtHandler(member.NewStore(d.Pool).WithSeats(
		lensintegration.NewSeats(lens, lenscreds.New(lensURL, testMintKey))))
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(testGWSecret, exempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), exempt))
		h.Mount(r)
	})
	return r
}

func memberCount(t *testing.T, d *testutil.DB, wsID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM members WHERE workspace_id = $1`, wsID).Scan(&n); err != nil {
		t.Fatalf("count members: %v", err)
	}
	return n
}

// addAs adds email to the Track workspace wsID as owner, through the gateway naming lensWS as the Lens workspace
// the owner's plan is billed under ("" sends no X-Lens-Workspace).
func addAs(h http.Handler, wsID, lensWS, owner, email string) *httptest.ResponseRecorder {
	r := mreq(http.MethodPost, "/v1/workspaces/"+wsID+"/members", `{"email":"`+email+`"}`, owner)
	if lensWS != "" {
		r.Header.Set(gatewayauth.HeaderLensWorkspace, lensWS)
	}
	return do(h, r)
}

// A Free workspace's second member is refused with Lens's message, unchanged, and nothing is written.
func TestAddMember_FreeSecondMemberRefusedWithLensMessage(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "owner@x.com", authz.RoleOwner)
	h := seatsChain(d, newLensSeatsStub(t, map[string]string{"lws_free": "free"}).URL)

	rr := addAs(h, ws.ID, "lws_free", "owner@x.com", "bob@x.com")
	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("second member on free = %d, want 402; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	want := "LENS_PLAN_GATES: the free plan allows 1 seat — the team plan allows 5 seats"
	if out["error"] != want || out["plan"] != "free" || out["allows"] != "team" || out["code"] != "PLAN_SEATS" {
		t.Fatalf("refusal = %v, want Lens's error %q with plan free, allows team", out, want)
	}
	if n := memberCount(t, d, ws.ID); n != 1 {
		t.Fatalf("members after the refusal = %d, want 1 (nothing written)", n)
	}
}

// B32.73: an owner whose Lens workspace is on Team adds a fifth member and is refused a sixth. Lens is asked about
// the Lens workspace the gateway names, never Track's own id — which the stub has on Free, as an unsubscribed
// workspace is, so asking about it would refuse the second member.
func TestAddMember_TeamFifthAddedSixthRefused_AskingAboutTheLensWorkspace(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "owner@x.com", authz.RoleOwner)
	lens := newLensSeatsStub(t, map[string]string{"lws_team": "team", ws.ID: "free"})
	h := seatsChain(d, lens.URL)

	for i := 2; i <= 5; i++ {
		if rr := addAs(h, ws.ID, "lws_team", "owner@x.com", fmt.Sprintf("m%d@x.com", i)); rr.Code != http.StatusCreated {
			t.Fatalf("member %d on team = %d, want 201; body=%s", i, rr.Code, rr.Body.String())
		}
	}
	rr := addAs(h, ws.ID, "lws_team", "owner@x.com", "m6@x.com")
	if rr.Code != http.StatusPaymentRequired || !strings.Contains(rr.Body.String(), "LENS_PLAN_GATES: the team plan allows 5 seats — the business plan allows 25 seats") {
		t.Fatalf("sixth member on team = %d, want 402 naming the team plan; body=%s", rr.Code, rr.Body.String())
	}
	if n := memberCount(t, d, ws.ID); n != 5 {
		t.Fatalf("members after the sixth was refused = %d, want 5", n)
	}
	asked := lens.askedAbout()
	if len(asked) != 5 {
		t.Fatalf("Lens was asked %d times (%v), want 5 — once per add", len(asked), asked)
	}
	for _, a := range asked {
		if a != "lws_team" {
			t.Fatalf("Lens was asked about %q (all: %v), want only the Lens workspace lws_team — never Track's id %s", a, asked, ws.ID)
		}
	}
}

// With Lens deployed but no Lens workspace on the request, the add is refused as unchecked and nothing is written:
// a plan nobody asked about is not a plan that passed.
func TestAddMember_NoLensWorkspaceRefusedUnchecked(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "owner@x.com", authz.RoleOwner)
	lens := newLensSeatsStub(t, map[string]string{ws.ID: "team"})
	h := seatsChain(d, lens.URL)

	rr := addAs(h, ws.ID, "", "owner@x.com", "bob@x.com")
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "SEATS_UNCHECKED") {
		t.Fatalf("add without X-Lens-Workspace = %d, want 503 SEATS_UNCHECKED; body=%s", rr.Code, rr.Body.String())
	}
	if n := memberCount(t, d, ws.ID); n != 1 {
		t.Fatalf("members after the unchecked add = %d, want 1 (nothing written)", n)
	}
	if asked := lens.askedAbout(); len(asked) != 0 {
		t.Fatalf("Lens was asked about %v, want nothing — Track's own id is never sent", asked)
	}
}
