package lenscreds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const testAdminKey = "tlv_admin_shared_key"

// fakeClock is a mutex-guarded settable clock. The provider's expiry
// logic reads it (p.now) and the fake Lens computes expires_at from it,
// so a test can advance time deterministically and drive refresh.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *fakeClock { return &fakeClock{t: t} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// barrier releases every waiter once n of them have arrived, or reports
// false to each waiter after timeout. It lets a test PROVE that two
// mints ran concurrently: if the provider serialized different
// workspaces, the second waiter never arrives, the first times out, and
// the assertion fails instead of hanging.
type barrier struct {
	n     int
	mu    sync.Mutex
	count int
	ready chan struct{}
}

func newBarrier(n int) *barrier { return &barrier{n: n, ready: make(chan struct{})} }
func (b *barrier) wait(timeout time.Duration) bool {
	b.mu.Lock()
	b.count++
	if b.count == b.n {
		close(b.ready)
	}
	b.mu.Unlock()
	select {
	case <-b.ready:
		return true
	case <-time.After(timeout):
		return false
	}
}

// fakeLens is an httptest server that serves ONLY the admin-gated mint
// endpoint POST /v1/auth/token. It records every mint's decoded body +
// auth header so tests can assert the credential and workspace claim.
type fakeLens struct {
	clock       *fakeClock
	server      *httptest.Server
	mu          sync.Mutex
	mints       int
	byWorkspace map[string]int
	authSeen    []string
	lastTTL     int
	onMint      func(workspaceID string) // optional in-handler hook (barriers / sleeps)
	failStatus  int                      // if != 0, respond this status with no token
}

func newFakeLens(t *testing.T, clock *fakeClock) *fakeLens {
	t.Helper()
	f := &fakeLens{clock: clock, byWorkspace: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeLens) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/token" {
		http.NotFound(w, r)
		return
	}
	auth := r.Header.Get("Authorization")
	var body struct {
		WorkspaceID string `json:"workspace_id"`
		TTLHours    int    `json:"ttl_hours"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if f.onMint != nil {
		f.onMint(body.WorkspaceID)
	}
	f.mu.Lock()
	f.mints++
	f.byWorkspace[body.WorkspaceID]++
	f.authSeen = append(f.authSeen, auth)
	f.lastTTL = body.TTLHours
	n := f.mints
	f.mu.Unlock()

	if f.failStatus != 0 {
		w.WriteHeader(f.failStatus)
		return
	}
	exp := f.clock.Now().Add(time.Duration(body.TTLHours) * time.Hour)
	// Token encodes ws + mint ordinal so tests can distinguish a cached
	// token (same ordinal) from a re-minted one (higher ordinal) and
	// prove per-workspace isolation.
	tok := "jwt." + body.WorkspaceID + "." + itoa(n)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      tok,
		"expires_at": exp.Format(time.RFC3339Nano),
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func (f *fakeLens) mintCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mints
}

// newProvider wires a provider to the fake Lens and drives its expiry
// clock from the same fake clock, so refresh is deterministic.
func newProvider(f *fakeLens) *Provider {
	p := New(f.server.URL, testAdminKey)
	p.now = f.clock.Now
	return p
}

func TestTokenFor_MintsThenCaches(t *testing.T) {
	clock := newClock(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	f := newFakeLens(t, clock)
	p := newProvider(f)

	tok1, err := p.TokenFor(context.Background(), "ws-A")
	if err != nil {
		t.Fatalf("first TokenFor: %v", err)
	}
	if tok1 == "" {
		t.Fatal("first token is empty")
	}
	if tok1 == testAdminKey {
		t.Fatal("provider returned the ADMIN key on a data path — must never happen")
	}
	if f.mintCount() != 1 {
		t.Fatalf("expected exactly 1 mint, got %d", f.mintCount())
	}

	tok2, err := p.TokenFor(context.Background(), "ws-A")
	if err != nil {
		t.Fatalf("second TokenFor: %v", err)
	}
	if tok2 != tok1 {
		t.Errorf("second call re-minted (%q != %q); expected the cached token", tok2, tok1)
	}
	if f.mintCount() != 1 {
		t.Errorf("second call must be served from cache; mint count = %d, want 1", f.mintCount())
	}
}

func TestTokenFor_MintCarriesAdminKeyAndWorkspace(t *testing.T) {
	clock := newClock(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	f := newFakeLens(t, clock)
	p := newProvider(f)

	if _, err := p.TokenFor(context.Background(), "ws-A"); err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.authSeen) != 1 {
		t.Fatalf("expected 1 mint, saw %d", len(f.authSeen))
	}
	if f.authSeen[0] != "Bearer "+testAdminKey {
		t.Errorf("mint Authorization = %q, want Bearer <adminKey> (mint endpoint is admin-gated)", f.authSeen[0])
	}
	if f.byWorkspace["ws-A"] != 1 {
		t.Errorf("mint body workspace_id must be ws-A; byWorkspace=%v", f.byWorkspace)
	}
	if f.lastTTL != tokenTTLHours {
		t.Errorf("mint body ttl_hours = %d, want %d", f.lastTTL, tokenTTLHours)
	}
}

func TestTokenFor_RefreshesBeforeExpiry(t *testing.T) {
	clock := newClock(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	f := newFakeLens(t, clock)
	p := newProvider(f)

	tok1, err := p.TokenFor(context.Background(), "ws-A")
	if err != nil {
		t.Fatalf("first TokenFor: %v", err)
	}
	// Cross the refresh threshold: exp = now + ttl; threshold = exp - skew.
	clock.Advance(time.Duration(tokenTTLHours)*time.Hour - refreshSkew + time.Minute)

	tok2, err := p.TokenFor(context.Background(), "ws-A")
	if err != nil {
		t.Fatalf("refresh TokenFor: %v", err)
	}
	if f.mintCount() != 2 {
		t.Errorf("token past refresh threshold must re-mint; mint count = %d, want 2", f.mintCount())
	}
	if tok2 == tok1 {
		t.Errorf("refresh returned the stale token %q; expected a freshly minted one", tok2)
	}
}

func TestTokenFor_NoRefreshBeforeThreshold(t *testing.T) {
	clock := newClock(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	f := newFakeLens(t, clock)
	p := newProvider(f)

	tok1, err := p.TokenFor(context.Background(), "ws-A")
	if err != nil {
		t.Fatalf("first TokenFor: %v", err)
	}
	// Stay comfortably before the refresh threshold.
	clock.Advance(time.Duration(tokenTTLHours)*time.Hour - refreshSkew - time.Minute)

	tok2, err := p.TokenFor(context.Background(), "ws-A")
	if err != nil {
		t.Fatalf("second TokenFor: %v", err)
	}
	if f.mintCount() != 1 {
		t.Errorf("token still fresh must NOT re-mint; mint count = %d, want 1", f.mintCount())
	}
	if tok2 != tok1 {
		t.Errorf("expected the cached token before the threshold")
	}
}

func TestTokenFor_IsolatesWorkspaces(t *testing.T) {
	clock := newClock(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	f := newFakeLens(t, clock)
	p := newProvider(f)

	tokA, err := p.TokenFor(context.Background(), "ws-A")
	if err != nil {
		t.Fatalf("TokenFor ws-A: %v", err)
	}
	tokB, err := p.TokenFor(context.Background(), "ws-B")
	if err != nil {
		t.Fatalf("TokenFor ws-B: %v", err)
	}
	if tokA == tokB {
		t.Errorf("two workspaces got the SAME token (%q) — attribution would collapse", tokA)
	}
	if f.mintCount() != 2 {
		t.Errorf("distinct workspaces must mint separately; mint count = %d, want 2", f.mintCount())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byWorkspace["ws-A"] != 1 || f.byWorkspace["ws-B"] != 1 {
		t.Errorf("each workspace must mint once with its own id; byWorkspace=%v", f.byWorkspace)
	}
}

func TestTokenFor_CoalescesConcurrentSameWorkspace(t *testing.T) {
	clock := newClock(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	f := newFakeLens(t, clock)
	// Widen the mint window so, absent coalescing, many goroutines would
	// enter the handler concurrently.
	f.onMint = func(string) { time.Sleep(30 * time.Millisecond) }
	p := newProvider(f)

	const goroutines = 50
	var wg sync.WaitGroup
	toks := make([]string, goroutines)
	errs := make([]error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			toks[i], errs[i] = p.TokenFor(context.Background(), "ws-A")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if f.mintCount() != 1 {
		t.Errorf("concurrent same-workspace calls must coalesce to ONE mint; got %d", f.mintCount())
	}
	for i, tok := range toks {
		if tok != toks[0] {
			t.Fatalf("goroutine %d got a different token (%q != %q)", i, tok, toks[0])
		}
	}
}

func TestTokenFor_DifferentWorkspacesMintConcurrently(t *testing.T) {
	clock := newClock(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	f := newFakeLens(t, clock)
	// A 2-arrival barrier: if the provider serialized different
	// workspaces, only one mint arrives, the barrier times out, and the
	// concurrency assertion below fails (rather than hanging forever).
	b := newBarrier(2)
	var observed sync.Map // workspaceID -> bool (barrier released)
	f.onMint = func(ws string) { observed.Store(ws, b.wait(2*time.Second)) }
	p := newProvider(f)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = p.TokenFor(context.Background(), "ws-A") }()
	go func() { defer wg.Done(); _, _ = p.TokenFor(context.Background(), "ws-B") }()
	wg.Wait()

	if f.mintCount() != 2 {
		t.Errorf("distinct workspaces must each mint; got %d", f.mintCount())
	}
	for _, ws := range []string{"ws-A", "ws-B"} {
		v, ok := observed.Load(ws)
		if !ok || v != true {
			t.Errorf("workspace %s did not mint concurrently with the other (serialized) — barrier not released", ws)
		}
	}
}

func TestTokenFor_MintFailureFailsClosedNeverAdminKey(t *testing.T) {
	clock := newClock(time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC))
	f := newFakeLens(t, clock)
	f.failStatus = http.StatusInternalServerError
	p := newProvider(f)

	tok, err := p.TokenFor(context.Background(), "ws-A")
	if err == nil {
		t.Fatal("expected an error when minting fails (fail-closed)")
	}
	if tok != "" {
		t.Errorf("mint failure returned a non-empty token %q; must be empty", tok)
	}
	if tok == testAdminKey {
		t.Fatal("mint failure fell back to the ADMIN key — the exact collapse this fix prevents")
	}
	if f.mintCount() < 1 {
		t.Errorf("the mint endpoint should have been contacted; mint count = %d", f.mintCount())
	}
}
