package dbresil

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// ── Breaker ────────────────────────────────────────────────────────────────

func TestBreaker_OpensAfterThreshold(t *testing.T) {
	b := NewBreaker(3)
	b.RecordFailure()
	b.RecordFailure()
	if !b.Allow() {
		t.Fatal("breaker opened before reaching the failure threshold")
	}
	b.RecordFailure() // 3rd consecutive
	if b.Allow() {
		t.Fatal("breaker should be open after 3 consecutive failures")
	}
	if b.State() != "open" {
		t.Errorf("State = %q, want open", b.State())
	}
}

func TestBreaker_ClosesOnSuccess(t *testing.T) {
	b := NewBreaker(2)
	b.RecordFailure()
	b.RecordFailure()
	if b.Allow() {
		t.Fatal("precondition: breaker should be open")
	}
	b.RecordSuccess() // a successful probe (DB recovered)
	if !b.Allow() {
		t.Fatal("breaker should close on a successful probe")
	}
	if b.State() != "closed" {
		t.Errorf("State = %q, want closed", b.State())
	}
}

func TestBreaker_SuccessResetsFailureRun(t *testing.T) {
	b := NewBreaker(3)
	b.RecordFailure()
	b.RecordFailure()
	b.RecordSuccess() // resets the consecutive-failure run
	b.RecordFailure()
	b.RecordFailure()
	if !b.Allow() {
		t.Fatal("two failures after a success must not re-open a threshold-3 breaker")
	}
}

func TestBreaker_OnStateChangeFires(t *testing.T) {
	var mu sync.Mutex
	var transitions []bool
	b := NewBreaker(1).OnStateChange(func(open bool) {
		mu.Lock()
		transitions = append(transitions, open)
		mu.Unlock()
	})
	b.RecordFailure() // → open
	b.RecordSuccess() // → closed
	mu.Lock()
	defer mu.Unlock()
	if len(transitions) != 2 || transitions[0] != true || transitions[1] != false {
		t.Fatalf("transitions = %v, want [true false]", transitions)
	}
}

// ── Monitor ────────────────────────────────────────────────────────────────

type fakePinger struct {
	mu  sync.Mutex
	err error
}

func (f *fakePinger) set(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }
func (f *fakePinger) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestMonitor_OpensWhenPingFails(t *testing.T) {
	p := &fakePinger{err: errors.New("connection refused")}
	b := NewBreaker(2)
	m := NewMonitor(p, b, 10*time.Millisecond, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	if !waitFor(t, func() bool { return !b.Allow() }) {
		t.Fatal("breaker did not open while pings were failing")
	}
}

// TestMonitor_RecoversWhenPingSucceeds is the "retries and recovers" guarantee:
// the monitor keeps probing a down DB (opening the breaker), then closes it once
// Postgres answers again.
func TestMonitor_RecoversWhenPingSucceeds(t *testing.T) {
	p := &fakePinger{err: errors.New("connection refused")}
	b := NewBreaker(2)
	m := NewMonitor(p, b, 10*time.Millisecond, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	if !waitFor(t, func() bool { return !b.Allow() }) {
		t.Fatal("breaker did not open while DB was down")
	}
	p.set(nil) // DB recovers
	if !waitFor(t, func() bool { return b.Allow() }) {
		t.Fatal("breaker did not recover after the DB came back")
	}
}

// ── Guard middleware ─────────────────────────────────────────────────────────

func TestGuard_PassesWhenClosed(t *testing.T) {
	b := NewBreaker(1) // closed
	called := false
	h := Guard(b)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/issues", nil))
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("closed breaker should pass through: called=%v code=%d", called, rec.Code)
	}
}

// The headline T15 guarantee: when the DB is down (breaker open), a request gets
// a proper 503 — never reaching the handler, never a raw 400, never a hang.
func TestGuard_503WhenOpen(t *testing.T) {
	b := NewBreaker(1)
	b.RecordFailure() // open

	called := false
	h := Guard(b)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/issues", nil))

	if called {
		t.Fatal("handler must NOT run while the DB breaker is open")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if body := rec.Body.String(); !contains(body, "DB_UNAVAILABLE") {
		t.Errorf("body = %q, want a DB_UNAVAILABLE code", body)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (stringIndex(s, sub) >= 0) }

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
