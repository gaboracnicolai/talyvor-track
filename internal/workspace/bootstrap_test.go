package workspace_test

// Bootstrap: a signed-in identity with no workspace gets its first one.
//
// WHY THIS IS NOT `POST /v1/workspaces`, WHICH ALREADY EXISTS. That route is
// USER-DRIVEN and unconditional: the client names the workspace and its slug,
// and every call creates another one. It is the right shape for "make me a
// second workspace called Platform" and the wrong shape for login, where the
// caller supplies nothing, the same person arrives repeatedly, and the second
// arrival must find the first workspace rather than make another. Bootstrap is
// therefore a distinct operation with a distinct property — idempotence — not a
// flag on the existing one.
//
// IDEMPOTENCE IS THE WHOLE CORRECTNESS PROPERTY, so it is asserted three ways:
// sequentially (same identity twice), concurrently (two logins racing), and
// after a failure (a retry converges rather than doubling). Anything less
// leaves "one person, two workspaces" reachable.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workspace"
)

// bootstrapReq builds a request carrying a VERIFIED identity, exactly as
// gatewayauth.Middleware leaves it after checking X-Gateway-Auth. The route
// reads nothing else — no body, no query, no path parameter.
func bootstrapReq(issuer, subject, email string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/bootstrap", nil)
	return r.WithContext(gatewayauth.WithIdentity(r.Context(), gatewayauth.Identity{
		Issuer: issuer, UserID: subject, Email: email,
	}))
}

func bootstrapOnce(t *testing.T, h *workspace.Handler, issuer, sub, email string) (int, workspace.BootstrapResult) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, bootstrapReq(issuer, sub, email))
	var out workspace.BootstrapResult
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("bootstrap: unreadable response %q: %v", rr.Body.String(), err)
		}
	}
	return rr.Code, out
}

func countWorkspaces(t *testing.T, d *testutil.DB) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(), `SELECT count(*) FROM workspaces`).Scan(&n); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	return n
}

func countOwners(t *testing.T, d *testutil.DB, wsID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM members WHERE workspace_id=$1 AND role='owner'`, wsID).Scan(&n); err != nil {
		t.Fatalf("count owners: %v", err)
	}
	return n
}

/* ── Idempotence ─────────────────────────────────────────────────────────── */

// TestBootstrap_SameIdentityTwice_OneWorkspaceOneOwner is the property the whole
// route exists for: a person logging in a second time must land in the workspace
// they already have.
func TestBootstrap_SameIdentityTwice_OneWorkspaceOneOwner(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	code1, first := bootstrapOnce(t, h, "https://idp.example.com", "sub-1", "a@example.com")
	if code1 != http.StatusOK {
		t.Fatalf("first bootstrap: got %d, want 200", code1)
	}
	if !first.Created {
		t.Error("first bootstrap must report created=true")
	}

	code2, second := bootstrapOnce(t, h, "https://idp.example.com", "sub-1", "a@example.com")
	if code2 != http.StatusOK {
		t.Fatalf("second bootstrap: got %d, want 200", code2)
	}
	if second.Created {
		t.Error("second bootstrap must report created=false — it found the existing workspace")
	}

	if first.WorkspaceID != second.WorkspaceID {
		t.Errorf("second login landed in a DIFFERENT workspace: %s vs %s — this is the defect",
			first.WorkspaceID, second.WorkspaceID)
	}
	if n := countWorkspaces(t, d); n != 1 {
		t.Errorf("workspaces = %d, want exactly 1", n)
	}
	if n := countOwners(t, d, first.WorkspaceID); n != 1 {
		t.Errorf("owner rows = %d, want exactly 1", n)
	}
}

// TestBootstrap_ConcurrentLogins_OneWorkspace: two tabs, one person, at the same
// instant. A check-then-create with no database constraint behind it would make
// two workspaces here — the slug UNIQUE index is what makes the loser converge on
// the winner's row instead of creating its own.
func TestBootstrap_ConcurrentLogins_OneWorkspace(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	const n = 8
	ids := make([]string, n)
	codes := make([]int, n)
	// A barrier so all n fire together. Without it they start staggered, the first
	// one wins outright, and the rest take the cheap lookup path — meaning the
	// UNIQUE-violation backstop is never exercised and the test would pass with it
	// removed (found by positive-controlling this test).
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			h.Bootstrap(rr, bootstrapReq("https://idp.example.com", "race-sub", "race@example.com"))
			codes[i] = rr.Code
			var out workspace.BootstrapResult
			_ = json.Unmarshal(rr.Body.Bytes(), &out)
			ids[i] = out.WorkspaceID
		}(i)
	}
	close(start)
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("goroutine %d: got %d — a racing login must succeed, not error", i, c)
		}
	}

	if got := countWorkspaces(t, d); got != 1 {
		t.Fatalf("concurrent bootstrap created %d workspaces, want exactly 1", got)
	}
	for i, id := range ids {
		if id == "" {
			t.Errorf("goroutine %d got no workspace id (code %d)", i, codes[i])
			continue
		}
		if id != ids[0] {
			t.Errorf("goroutine %d landed in a different workspace: %s vs %s", i, id, ids[0])
		}
	}
	if n := countOwners(t, d, ids[0]); n != 1 {
		t.Errorf("owner rows = %d after a race, want exactly 1", n)
	}
}

// TestBootstrap_RetryAfterFailureConverges. A Track blip must not be cached by
// the caller (see the BFF rule), which is only safe if retrying is safe. A failed
// bootstrap must leave NOTHING behind — in particular never a workspace with no
// owner, which T10 would make unreachable to everyone including its creator.
func TestBootstrap_RetryAfterFailureConverges(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	// A bootstrap that cannot succeed: no verified identity at all.
	rr := httptest.NewRecorder()
	h.Bootstrap(rr, httptest.NewRequest(http.MethodPost, "/v1/bootstrap", nil))
	if rr.Code == http.StatusOK {
		t.Fatalf("bootstrap without a verified identity: got 200, want a refusal")
	}
	if n := countWorkspaces(t, d); n != 0 {
		t.Fatalf("a FAILED bootstrap left %d workspaces behind — a retry would now double", n)
	}

	// The retry, now with an identity, converges on exactly one.
	if code, _ := bootstrapOnce(t, h, "https://idp.example.com", "sub-r", "r@example.com"); code != http.StatusOK {
		t.Fatalf("retry: got %d, want 200", code)
	}
	if n := countWorkspaces(t, d); n != 1 {
		t.Errorf("after retry: %d workspaces, want 1", n)
	}
}

/* ── Isolation ───────────────────────────────────────────────────────────── */

// TestBootstrap_TwoIdentities_SeparateWorkspaces_NoCrossRead: the point of the
// change. Ten trial users must not share one Track.
func TestBootstrap_TwoIdentities_SeparateWorkspaces_NoCrossRead(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	_, a := bootstrapOnce(t, h, "https://idp.example.com", "sub-a", "a@example.com")
	_, b := bootstrapOnce(t, h, "https://idp.example.com", "sub-b", "b@example.com")

	if a.WorkspaceID == "" || b.WorkspaceID == "" {
		t.Fatal("both identities must get a workspace")
	}
	if a.WorkspaceID == b.WorkspaceID {
		t.Fatal("two identities landed in ONE workspace — the defect this removes")
	}
	if n := countWorkspaces(t, d); n != 2 {
		t.Errorf("workspaces = %d, want 2", n)
	}

	// Neither is a member of the other's workspace — which is what makes A unable
	// to read B's issues: authz.Middleware 403s any {wsID} the caller has no
	// membership for, and bootstrap seeds exactly one member per workspace.
	var crossed int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM members WHERE (workspace_id=$1 AND email=$2) OR (workspace_id=$3 AND email=$4)`,
		a.WorkspaceID, "b@example.com", b.WorkspaceID, "a@example.com").Scan(&crossed); err != nil {
		t.Fatalf("cross-membership query: %v", err)
	}
	if crossed != 0 {
		t.Errorf("found %d cross-workspace memberships — each identity must be a member of its own only", crossed)
	}
}

/* ── It is a route, not a middleware side effect ─────────────────────────── */

// TestBootstrap_IsNotASideEffectOfReading is the guard for the design decision
// most likely to be "simplified" later: putting auto-provisioning into
// authz.Middleware, where it would run on EVERY request from an unknown identity.
// That would make a security chokepoint write, would provision a tenant from a
// typo'd email, and would race on concurrent reads. Provisioning must happen only
// when it is explicitly asked for.
func TestBootstrap_IsNotASideEffectOfReading(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	// An unknown identity performs an ordinary read — the List route, which is the
	// no-{wsID} route a brand-new session hits first.
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	h.List(rr, r.WithContext(gatewayauth.WithIdentity(r.Context(), gatewayauth.Identity{
		Issuer: "https://idp.example.com", UserID: "sub-reader", Email: "reader@example.com",
	})))

	if n := countWorkspaces(t, d); n != 0 {
		t.Errorf("reading created %d workspaces — provisioning must never be a side effect of a read", n)
	}
}

/* ── Identity requirements ───────────────────────────────────────────────── */

// TestBootstrap_RequiresStableIdentity. The key is (issuer, subject), NOT email —
// the same choice Lens made, for the same reason: an email can be reassigned to a
// new person, and keying a tenant on it would eventually hand a new employee the
// previous holder's workspace. Fail closed when the gateway supplies no stable
// pair rather than silently falling back to the reassignable field.
func TestBootstrap_RequiresStableIdentity(t *testing.T) {
	for _, c := range []struct {
		name                   string
		issuer, subject, email string
	}{
		{"no identity at all", "", "", ""},
		{"email only — no stable subject", "", "", "only@example.com"},
		{"subject without issuer", "", "sub-x", "x@example.com"},
		{"issuer without subject", "https://idp.example.com", "", "x@example.com"},
		{"stable pair but no email for the owner row", "https://idp.example.com", "sub-x", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := testutil.New(t)
			h := workspace.NewHandler(workspace.NewStore(d.Pool))
			code, _ := bootstrapOnce(t, h, c.issuer, c.subject, c.email)
			if code == http.StatusOK {
				t.Errorf("got 200 — bootstrap must refuse an identity it cannot key on")
			}
			if n := countWorkspaces(t, d); n != 0 {
				t.Errorf("a refused bootstrap created %d workspaces", n)
			}
		})
	}
}

// TestBootstrap_DifferentIssuersDoNotCollide: two IdPs can issue the same subject
// string. The pair must be unambiguous, or adding a second IdP would merge two
// people into one workspace.
func TestBootstrap_DifferentIssuersDoNotCollide(t *testing.T) {
	d := testutil.New(t)
	h := workspace.NewHandler(workspace.NewStore(d.Pool))

	// The SAME email at both issuers, deliberately. With different emails this test
	// would also pass against an email-keyed implementation and would therefore
	// prove nothing about the issuer — caught by positive-controlling it.
	const shared = "shared@example.com"
	_, a := bootstrapOnce(t, h, "https://idp-one.example.com", "shared-sub", shared)
	_, b := bootstrapOnce(t, h, "https://idp-two.example.com", "shared-sub", shared)

	if a.WorkspaceID == "" || b.WorkspaceID == "" {
		t.Fatal("both identities must get a workspace")
	}
	if a.WorkspaceID == b.WorkspaceID {
		t.Fatal("same subject at two issuers collided onto ONE workspace — the key is not " +
			"including the issuer, so adding a second IdP would merge two people")
	}
}

/* ── Wiring: reachable without membership, still gateway-gated ───────────── */

// TestBootstrap_MountedOutsideTheWorkspacePrefix. The route must be reachable by a
// caller who has NO workspace — which is every caller it exists for. Mounted at
// /v1/workspaces/bootstrap it would not be: authz.workspaceIDFromPath reads the
// third segment as a {wsID}, and the membership check 403s before the handler
// runs. This asserts the path Track's own authorizer treats as workspace-scoped,
// so a later "tidy-up" that moves the route under /workspaces fails here rather
// than in production.
func TestBootstrap_MountedOutsideTheWorkspacePrefix(t *testing.T) {
	d := testutil.New(t)
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authz.Middleware(emptyResolver{}, func(string) bool { return false }))
		workspace.NewHandler(workspace.NewStore(d.Pool)).Mount(r)
	})

	// A verified identity with ZERO memberships — exactly a first login.
	req := httptest.NewRequest(http.MethodPost, "/v1/bootstrap", nil)
	req = req.WithContext(gatewayauth.WithIdentity(req.Context(), gatewayauth.Identity{
		Issuer: "https://idp.example.com", UserID: "sub-mount", Email: "mount@example.com",
	}))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code == http.StatusForbidden {
		t.Fatal("bootstrap 403'd a caller with no membership — the route is inside the " +
			"workspace-scoped prefix, where the callers it exists for can never reach it")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", rr.Code, rr.Body.String())
	}
	if n := countWorkspaces(t, d); n != 1 {
		t.Errorf("workspaces = %d, want 1", n)
	}
}

// emptyResolver stands in for the production membership resolver for an identity
// that has none yet.
type emptyResolver struct{}

func (emptyResolver) MembershipsByEmail(context.Context, string) ([]authz.Membership, error) {
	return nil, nil
}

// TestBootstrap_UniqueViolationBackstopIsWired asserts DETERMINISTICALLY what the
// concurrency test can only probabilistically reach: that the error a duplicate
// slug produces is the one the recovery path recognises. If CreateWithOwner ever
// returned a different error shape, the race recovery would silently stop working
// and only a lost race would reveal it.
func TestBootstrap_UniqueViolationBackstopIsWired(t *testing.T) {
	d := testutil.New(t)
	store := workspace.NewStore(d.Pool)

	const slug = "wdeterministicduplicate0000"
	if _, err := store.CreateWithOwner(context.Background(),
		model.Workspace{Name: "first", Slug: slug}, "first@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := store.CreateWithOwner(context.Background(),
		model.Workspace{Name: "second", Slug: slug}, "second@example.com")
	if err == nil {
		t.Fatal("a duplicate slug must be refused by the UNIQUE constraint — " +
			"without it the race backstop has nothing to catch")
	}
	if !workspace.IsUniqueViolationForTest(err) {
		t.Errorf("duplicate-slug error is not recognised as a unique violation (%v) — "+
			"the concurrent-login recovery path would not fire", err)
	}
}
