package member_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/member"
	"github.com/talyvor/track/internal/testutil"
)

// lensSeatsStub serves Lens's GET /v1/workspaces/{ws}/plan/seats?members=N (B32.12) with Lens's own shapes and
// wording, for the plan each workspace is on: free takes 1 seat, team 5.
func lensSeatsStub(t *testing.T, planOf map[string]string) *httptest.Server {
	t.Helper()
	seats := map[string]int64{"free": 1, "team": 5}
	next := map[string]string{"free": "team", "team": "business"}
	nextSeats := map[string]int64{"team": 5, "business": 25}
	r := chi.NewRouter()
	r.Get("/v1/workspaces/{ws}/plan/seats", func(w http.ResponseWriter, req *http.Request) {
		plan := planOf[chi.URLParam(req, "ws")]
		n, _ := strconv.ParseInt(req.URL.Query().Get("members"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
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
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func seatsChain(d *testutil.DB, lensURL string) http.Handler {
	exempt := func(string) bool { return false }
	h := member.NewMgmtHandler(member.NewStore(d.Pool).WithSeats(lensintegration.New(lensURL, "admin-key")))
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

func addAs(h http.Handler, wsID, owner, email string) *httptest.ResponseRecorder {
	return do(h, mreq(http.MethodPost, "/v1/workspaces/"+wsID+"/members", `{"email":"`+email+`"}`, owner))
}

// A Free workspace's second member is refused with Lens's message, unchanged, and nothing is written.
func TestAddMember_FreeSecondMemberRefusedWithLensMessage(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "owner@x.com", authz.RoleOwner)
	h := seatsChain(d, lensSeatsStub(t, map[string]string{ws.ID: "free"}).URL)

	rr := addAs(h, ws.ID, "owner@x.com", "bob@x.com")
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

// A Team workspace adds its fifth member and is refused its sixth.
func TestAddMember_TeamFifthAddedSixthRefused(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "owner@x.com", authz.RoleOwner)
	h := seatsChain(d, lensSeatsStub(t, map[string]string{ws.ID: "team"}).URL)

	for i := 2; i <= 5; i++ {
		if rr := addAs(h, ws.ID, "owner@x.com", fmt.Sprintf("m%d@x.com", i)); rr.Code != http.StatusCreated {
			t.Fatalf("member %d on team = %d, want 201; body=%s", i, rr.Code, rr.Body.String())
		}
	}
	rr := addAs(h, ws.ID, "owner@x.com", "m6@x.com")
	if rr.Code != http.StatusPaymentRequired || !strings.Contains(rr.Body.String(), "LENS_PLAN_GATES: the team plan allows 5 seats — the business plan allows 25 seats") {
		t.Fatalf("sixth member on team = %d, want 402 naming the team plan; body=%s", rr.Code, rr.Body.String())
	}
	if n := memberCount(t, d, ws.ID); n != 5 {
		t.Fatalf("members after the sixth was refused = %d, want 5", n)
	}
}
