package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/featureboard"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/guest"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// unauthenticated_surface_realpg_test.go — the seven exempt routes NOTHING had ever executed.
//
// exempt_route_census_test.go records, for each of the 14 routes that skip gwAuth+wsAuthz, what
// authenticates it instead. W3.39 then measured that SEVEN of those 14 are executed by no test at
// all, so their recorded auth was a claim derived by READING. This file asks whether it is TRUE,
// by sending real unauthenticated requests through the real router.
//
// ⚠ WHY IT MOUNTS PRODUCTION'S `Mount` AND DERIVES THE PREDICATE FROM main.go. W3.28 measured that
// this repository's tests overwhelmingly re-declare a route on a router they build themselves —
// internal/mcp registers `{workspaceID}` where production registers `{wsID}` — which exercises the
// handler while pinning nothing about the shipped registration. A test written that way here would
// be worse than useless: it would report the unauthenticated surface as verified while testing a
// router the product does not ship. So:
//
//	· the exempt predicate is built from gwExemptPrefixes(t), PARSED OUT OF main.go, not retyped —
//	  a route that silently stopped being exempt changes this test's behaviour rather than hiding;
//	· the routes come from featureboard/guest `Mount(r)` under /v1, exactly as main.go mounts them;
//	· both middlewares are installed, and the requests carry NO credentials of any kind.
//
// ⚠ EVERY REFUSAL IS PAIRED WITH A POSITIVE ON THE SAME FIXTURE. A 404 for a private board passes
// just as well when the fixture never created a board, so each denial is asserted beside an
// otherwise-identical request that MUST succeed. An unpaired refusal test proves nothing.

const testGatewaySecret = "w340-gateway-secret"

// unauthedRouter builds the shipped shape: /v1 with gwAuth+wsAuthz, exempting exactly the prefixes
// main.go exempts.
func unauthedRouter(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	prefixes := gwExemptPrefixes(t)
	exempt := func(p string) bool {
		for _, pre := range prefixes {
			if strings.HasPrefix(p, pre) {
				return true
			}
		}
		return false
	}
	issueStore := issue.NewStore(pool)
	fbHandler := featureboard.NewHandler(featureboard.NewStore(pool), issueStore)
	gHandler := guest.NewHandler(guest.NewStore(pool, "w340-guest-secret"), issueStore, "")

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(testGatewaySecret, exempt))
		r.Use(authz.Middleware(authz.NewPGResolver(pool), exempt))
		fbHandler.Mount(r)
		gHandler.Mount(r)
	})
	return r
}

// anon issues a request carrying NO credentials — no gateway proof, no user email, no bearer.
func anon(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestUnauthenticatedSurface_ExemptRoutesEnforceTheirOwnAuth drives every one of the seven.
func TestUnauthenticatedSurface_ExemptRoutesEnforceTheirOwnAuth(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	h := unauthedRouter(t, d.Pool)
	fb := featureboard.NewStore(d.Pool)

	// ── fixture ────────────────────────────────────────────────────────────────────────────────
	// Workspace A hosts a PUBLIC board; workspace B hosts a PRIVATE one. Both carry a post, so
	// every denial below has a structurally identical subject that is allowed.
	wsA := d.Workspace(t)
	wsB := d.Workspace(t)
	slugA := workspaceSlug(t, d.Pool, wsA.ID)
	slugB := workspaceSlug(t, d.Pool, wsB.ID)

	pubBoard, err := fb.CreateBoard(ctx, featureboard.Board{
		WorkspaceID: wsA.ID, Name: "Public", Slug: "public-board", Public: true, AllowAnonymous: true,
	})
	if err != nil {
		t.Fatalf("create public board: %v", err)
	}
	// A public board that has NOT opted into anonymous posting.
	namedBoard, err := fb.CreateBoard(ctx, featureboard.Board{
		WorkspaceID: wsA.ID, Name: "Named", Slug: "named-board", Public: true, AllowAnonymous: false,
	})
	if err != nil {
		t.Fatalf("create named board: %v", err)
	}
	privBoard, err := fb.CreateBoard(ctx, featureboard.Board{
		WorkspaceID: wsB.ID, Name: "Private", Slug: "private-board", Public: false, AllowAnonymous: true,
	})
	if err != nil {
		t.Fatalf("create private board: %v", err)
	}
	pubPost, err := fb.CreatePost(ctx, featureboard.FeaturePost{
		WorkspaceID: wsA.ID, BoardID: pubBoard.ID, Title: "Public post", AuthorEmail: "a@corp.com",
	})
	if err != nil {
		t.Fatalf("create public post: %v", err)
	}
	privPost, err := fb.CreatePost(ctx, featureboard.FeaturePost{
		WorkspaceID: wsB.ID, BoardID: privBoard.ID, Title: "Private roadmap", AuthorEmail: "b@corp.com",
	})
	if err != nil {
		t.Fatalf("create private post: %v", err)
	}

	// ── 1. GET /v1/public/boards/{ws}/{board}/ — anonymous read, bounded by b.public ────────────
	t.Run("public board is readable anonymously and a private one is not", func(t *testing.T) {
		ok := anon(t, h, http.MethodGet, "/v1/public/boards/"+slugA+"/"+pubBoard.Slug+"/", nil)
		if ok.Code != http.StatusOK {
			t.Fatalf("PUBLIC board anonymous read = %d %s, want 200. The positive half of this "+
				"pair is what makes the refusal below meaningful.", ok.Code, body(ok))
		}
		denied := anon(t, h, http.MethodGet, "/v1/public/boards/"+slugB+"/"+privBoard.Slug+"/", nil)
		if denied.Code != http.StatusNotFound {
			t.Errorf("PRIVATE board anonymous read = %d %s, want 404. `AND b.public = true` is the "+
				"only thing standing between an unauthenticated caller and a private board.",
				denied.Code, body(denied))
		}
	})

	// ── 2. GET .../posts — same filter on the list ──────────────────────────────────────────────
	t.Run("posts list obeys the same public filter", func(t *testing.T) {
		ok := anon(t, h, http.MethodGet, "/v1/public/boards/"+slugA+"/"+pubBoard.Slug+"/posts", nil)
		if ok.Code != http.StatusOK {
			t.Fatalf("public posts list = %d %s, want 200", ok.Code, body(ok))
		}
		denied := anon(t, h, http.MethodGet, "/v1/public/boards/"+slugB+"/"+privBoard.Slug+"/posts", nil)
		if denied.Code == http.StatusOK && strings.Contains(body(denied), "Private roadmap") {
			t.Errorf("a PRIVATE board's posts were served to an anonymous caller: %s", body(denied))
		}
		if denied.Code != http.StatusNotFound {
			t.Errorf("private posts list = %d %s, want 404", denied.Code, body(denied))
		}
	})

	// ── 3. POST .../posts — ANONYMOUS WRITE, bounded by public + AllowAnonymous ─────────────────
	t.Run("anonymous post requires an email unless the board opted in", func(t *testing.T) {
		ok := anon(t, h, http.MethodPost, "/v1/public/boards/"+slugA+"/"+pubBoard.Slug+"/posts",
			map[string]string{"title": "anon idea"})
		if ok.Code != http.StatusCreated {
			t.Fatalf("anonymous post to an AllowAnonymous board = %d %s, want 201", ok.Code, body(ok))
		}
		needsEmail := anon(t, h, http.MethodPost, "/v1/public/boards/"+slugA+"/"+namedBoard.Slug+"/posts",
			map[string]string{"title": "no email"})
		if needsEmail.Code != http.StatusBadRequest || !strings.Contains(body(needsEmail), "EMAIL_REQUIRED") {
			t.Errorf("email-less post to a board WITHOUT AllowAnonymous = %d %s, want 400 "+
				"EMAIL_REQUIRED", needsEmail.Code, body(needsEmail))
		}
		toPrivate := anon(t, h, http.MethodPost, "/v1/public/boards/"+slugB+"/"+privBoard.Slug+"/posts",
			map[string]string{"title": "should not land", "author_email": "x@corp.com"})
		if toPrivate.Code != http.StatusNotFound {
			t.Errorf("anonymous WRITE to a PRIVATE board = %d %s, want 404", toPrivate.Code, body(toPrivate))
		}
	})

	// ── 4/5. vote and unvote — object tenancy across the board boundary ─────────────────────────
	t.Run("vote and unvote refuse a post that is not on the named public board", func(t *testing.T) {
		ok := anon(t, h, http.MethodPost,
			"/v1/public/boards/"+slugA+"/"+pubBoard.Slug+"/posts/"+pubPost.ID+"/vote",
			map[string]string{"email": "voter@corp.com"})
		if ok.Code != http.StatusOK {
			t.Fatalf("vote on the board's OWN post = %d %s, want 200", ok.Code, body(ok))
		}
		// The dangerous shape: a public board's slug in the path, a foreign post id in the body.
		crossed := anon(t, h, http.MethodPost,
			"/v1/public/boards/"+slugA+"/"+pubBoard.Slug+"/posts/"+privPost.ID+"/vote",
			map[string]string{"email": "voter@corp.com"})
		if crossed.Code == http.StatusOK {
			t.Errorf("an anonymous vote MUTATED a post on a private board in another workspace "+
				"through a public board's slug: %s", body(crossed))
		}
		unvoteOK := anon(t, h, http.MethodDelete,
			"/v1/public/boards/"+slugA+"/"+pubBoard.Slug+"/posts/"+pubPost.ID+"/vote",
			map[string]string{"email": "voter@corp.com"})
		if unvoteOK.Code != http.StatusOK {
			t.Fatalf("unvote on the board's OWN post = %d %s, want 200", unvoteOK.Code, body(unvoteOK))
		}
		crossedUnvote := anon(t, h, http.MethodDelete,
			"/v1/public/boards/"+slugA+"/"+pubBoard.Slug+"/posts/"+privPost.ID+"/vote",
			map[string]string{"email": "voter@corp.com"})
		if crossedUnvote.Code == http.StatusOK {
			t.Errorf("an anonymous UNVOTE reached a post on a private board in another workspace: %s",
				body(crossedUnvote))
		}
	})

	// ── 6/7. the invite pair — the token IS the credential ──────────────────────────────────────
	t.Run("invite detail and accept treat the token as the credential", func(t *testing.T) {
		gs := guest.NewStore(d.Pool, "w340-guest-secret")
		inv, err := gs.CreateInvite(ctx, wsA.ID, nil, "guest@corp.com", guest.GuestRoleViewer, "owner@corp.com")
		if err != nil {
			t.Fatalf("create invite: %v", err)
		}
		ok := anon(t, h, http.MethodGet, "/v1/invite/"+inv.Token, nil)
		if ok.Code != http.StatusOK {
			t.Fatalf("GET /v1/invite/{valid} = %d %s, want 200", ok.Code, body(ok))
		}
		bad := anon(t, h, http.MethodGet, "/v1/invite/not-a-real-token", nil)
		if bad.Code != http.StatusNotFound {
			t.Errorf("GET /v1/invite/{bogus} = %d %s, want 404 — the token IS the credential",
				bad.Code, body(bad))
		}
		badAccept := anon(t, h, http.MethodPost, "/v1/invite/not-a-real-token/accept",
			map[string]string{"name": "Mallory"})
		if badAccept.Code == http.StatusCreated {
			t.Errorf("a bogus token ACCEPTED an invite and was granted workspace access: %s",
				body(badAccept))
		}
		// The positive half — and this is the route that grants access, so it is asserted to
		// actually return a usable credential rather than merely a 201.
		good := anon(t, h, http.MethodPost, "/v1/invite/"+inv.Token+"/accept",
			map[string]string{"name": "Guest"})
		if good.Code != http.StatusCreated {
			t.Fatalf("POST /v1/invite/{valid}/accept = %d %s, want 201", good.Code, body(good))
		}
		var out struct {
			WorkspaceID string `json:"workspace_id"`
			AccessToken string `json:"access_token"`
		}
		if err := json.Unmarshal(good.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode accept: %v", err)
		}
		if out.WorkspaceID != wsA.ID || out.AccessToken == "" {
			t.Errorf("accept returned workspace_id=%q token-empty=%v, want %q and a token",
				out.WorkspaceID, out.AccessToken == "", wsA.ID)
		}
		// Replay: the same token must not grant a second time.
		replay := anon(t, h, http.MethodPost, "/v1/invite/"+inv.Token+"/accept",
			map[string]string{"name": "Again"})
		if replay.Code == http.StatusCreated {
			t.Errorf("an ALREADY-ACCEPTED invite token granted access a second time: %s", body(replay))
		}
	})
}

func body(rec *httptest.ResponseRecorder) string {
	return strings.TrimSpace(rec.Body.String())
}

// workspaceSlug reads the slug the public board routes resolve on. The fixture creates workspaces
// through the shared harness, so the slug is whatever that assigns rather than a value this test
// invents — a hand-picked slug would test a row this product never writes.
func workspaceSlug(t *testing.T, pool *pgxpool.Pool, workspaceID string) string {
	t.Helper()
	var slug string
	if err := pool.QueryRow(context.Background(),
		`SELECT slug FROM workspaces WHERE id = $1`, workspaceID).Scan(&slug); err != nil {
		t.Fatalf("read workspace slug: %v", err)
	}
	if slug == "" {
		t.Fatal("workspace has an empty slug — every public-board URL below would be ambiguous")
	}
	return slug
}
