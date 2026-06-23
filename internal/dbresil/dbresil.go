// Package dbresil makes a Postgres outage degrade gracefully instead of
// surfacing as misleading 400s.
//
// Today every handler maps any store error to 400, so "Postgres is down" — a
// server problem — reaches the client as a 4xx client error. dbresil adds a
// circuit breaker driven by a background health Monitor and a Guard middleware
// that fast-fails with 503 while the DB is unreachable: a correct status, no
// per-request hang, and no dead-pool hammering. It complements T14 (/readyz
// drains the instance from the load balancer; dbresil handles the requests that
// are already in flight) and the per-query statement_timeout set in internal/db.
//
// Pattern mirrors the Lens internal/retry circuit breaker (closed/open +
// Allow/RecordSuccess/RecordFailure), adapted to a monitor-fed model: because
// the Monitor probes on a fixed cadence, that cadence IS the recovery probe, so
// no separate half-open trial state is needed — the breaker simply closes on the
// next successful probe.
package dbresil

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Default tuning. Conservative on opening (3 consecutive failed probes ≈ a real
// outage, not a single blip) and quick to probe (2s) so recovery is fast.
const (
	DefaultFailureThreshold = 3
	DefaultProbeInterval    = 2 * time.Second
	DefaultPingTimeout      = 2 * time.Second
)

// Pinger is the DB-health surface the Monitor probes. *pgxpool.Pool satisfies it
// unchanged, so dbresil never imports pgx.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Breaker is a two-state circuit breaker fed by the Monitor's periodic probes.
// closed = DB believed healthy (requests admitted); open = recent probes failed
// (requests fast-fail). It opens after failureThreshold consecutive failures and
// closes on the next success. Safe for concurrent use: the request path reads
// Allow() while the monitor goroutine writes Record*.
type Breaker struct {
	failureThreshold int

	mu               sync.Mutex
	open             bool
	consecutiveFails int
	openedAt         time.Time
	onChange         func(open bool)
}

// NewBreaker builds a closed breaker. A non-positive threshold falls back to the
// default so a misconfiguration can't make a single blip trip the circuit.
func NewBreaker(failureThreshold int) *Breaker {
	if failureThreshold <= 0 {
		failureThreshold = DefaultFailureThreshold
	}
	return &Breaker{failureThreshold: failureThreshold}
}

// OnStateChange registers a hook fired on every open↔closed transition (for
// structured logging). Returns the breaker for chaining.
func (b *Breaker) OnStateChange(fn func(open bool)) *Breaker {
	b.mu.Lock()
	b.onChange = fn
	b.mu.Unlock()
	return b
}

// RecordSuccess reports a healthy probe: clears the failure run and closes the
// breaker if it was open.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	b.consecutiveFails = 0
	transitioned := b.open
	b.open = false
	hook := b.onChange
	b.mu.Unlock()
	if transitioned && hook != nil {
		hook(false)
	}
}

// RecordFailure reports a failed probe: opens the breaker once the consecutive
// failures reach the threshold.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	b.consecutiveFails++
	var transitioned bool
	if !b.open && b.consecutiveFails >= b.failureThreshold {
		b.open = true
		b.openedAt = time.Now()
		transitioned = true
	}
	hook := b.onChange
	b.mu.Unlock()
	if transitioned && hook != nil {
		hook(true)
	}
}

// Allow reports whether requests should be admitted (true while closed).
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.open
}

// State returns "open" or "closed" for observability.
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		return "open"
	}
	return "closed"
}

// Monitor periodically pings the DB and feeds a Breaker, so the request path can
// fast-fail via Guard when Postgres is unreachable instead of every handler
// turning a connection error into a misleading 400.
type Monitor struct {
	pinger      Pinger
	breaker     *Breaker
	interval    time.Duration
	pingTimeout time.Duration
}

// NewMonitor builds a monitor. Non-positive interval/timeout fall back to defaults.
func NewMonitor(p Pinger, b *Breaker, interval, pingTimeout time.Duration) *Monitor {
	if interval <= 0 {
		interval = DefaultProbeInterval
	}
	if pingTimeout <= 0 {
		pingTimeout = DefaultPingTimeout
	}
	return &Monitor{pinger: p, breaker: b, interval: interval, pingTimeout: pingTimeout}
}

// Start probes once immediately (so the breaker reflects reality at boot), then
// on every interval until ctx is cancelled. Runs in its own goroutine.
func (m *Monitor) Start(ctx context.Context) {
	m.probe(ctx)
	go func() {
		t := time.NewTicker(m.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.probe(ctx)
			}
		}
	}()
}

// probe runs one health ping under a bounded timeout and feeds the breaker. The
// timeout matters: a wedged DB must not make the probe itself hang.
func (m *Monitor) probe(ctx context.Context) {
	pingCtx, cancel := context.WithTimeout(ctx, m.pingTimeout)
	defer cancel()
	if err := m.pinger.Ping(pingCtx); err != nil {
		m.breaker.RecordFailure()
		return
	}
	m.breaker.RecordSuccess()
}

// Guard is router middleware that fast-fails with 503 when the DB breaker is
// open, so a Postgres outage returns a correct 503 (not a raw 400) without the
// request hitting a dead pool. When closed it is a single atomic-guarded read,
// negligible on the hot path.
func Guard(b *Breaker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !b.Allow() {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "database temporarily unavailable",
					"code":  "DB_UNAVAILABLE",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
