// Package health provides Track's liveness/readiness probes and graceful-drain
// state, decoupled from main.go so the behaviour is unit-testable.
//
//   - Live  (/livez):  always 200 while the process is running. Liveness must NOT
//     depend on Postgres/Redis, or a transient dependency blip would make an
//     orchestrator kill an otherwise-healthy instance.
//   - Ready (/readyz): 200 only when this instance is active (not draining) AND
//     every dependency check passes; 503 otherwise. A load balancer uses /readyz
//     to pull a draining or degraded instance out of rotation — which is what
//     makes graceful drain on SIGTERM possible.
//
// The existing static /healthz is intentionally left untouched for backward
// compatibility; /livez and /readyz are additive. main.go owns only the thin
// wiring (mount the handlers, call Drain on SIGTERM) — all the decision logic
// lives here behind tests.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// readyCheckTimeout bounds the whole readiness probe so a wedged dependency
// can't make /readyz itself hang (which would defeat the point of the probe).
const readyCheckTimeout = 2 * time.Second

// Dep is one readiness dependency. Name appears in the /readyz body; Check
// returns nil when the dependency is healthy.
type Dep struct {
	Name  string
	Check func(ctx context.Context) error
}

// Pinger is anything that can be context-pinged. *pgxpool.Pool satisfies it, so
// PingDep adapts the database pool into a readiness Dep without this package
// importing pgx.
type Pinger interface {
	Ping(ctx context.Context) error
}

// PingDep builds a Dep that reports healthy when p.Ping succeeds. A nil p is a
// failed check (treated as "not configured") rather than a panic, so a partially
// wired server degrades to not-ready instead of crashing the probe.
func PingDep(name string, p Pinger) Dep {
	return Dep{Name: name, Check: func(ctx context.Context) error {
		if p == nil {
			return errors.New("not configured")
		}
		return p.Ping(ctx)
	}}
}

// Drainer holds this instance's serve/drain state. The zero value is active
// (ready to serve); once SetDraining flips it, /readyz reports 503. Safe for
// concurrent use.
type Drainer struct {
	draining atomic.Bool
}

// SetDraining marks the instance as draining. Idempotent.
func (d *Drainer) SetDraining() { d.draining.Store(true) }

// Active reports whether the instance is still accepting new work (i.e. not
// draining).
func (d *Drainer) Active() bool { return !d.draining.Load() }

// Drain performs the graceful-shutdown sequence: flip to draining (so /readyz
// starts returning 503 and the load balancer pulls this instance from
// rotation), pause settle so the LB actually observes that 503 before
// connections stop, then run shutdownFn (e.g. http.Server.Shutdown) bounded by
// ctx. Returns shutdownFn's error. A nil shutdownFn just flips the state.
func (d *Drainer) Drain(ctx context.Context, settle time.Duration, shutdownFn func(context.Context) error) error {
	d.SetDraining()
	if settle > 0 {
		t := time.NewTimer(settle)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
		}
	}
	if shutdownFn == nil {
		return nil
	}
	return shutdownFn(ctx)
}

// Handler serves /livez and /readyz over a fixed set of dependencies and a
// shared Drainer.
type Handler struct {
	version string
	drainer *Drainer
	deps    []Dep
	timeout time.Duration
}

// New builds a Handler. A nil drainer is replaced with a fresh (active) one, so
// callers that don't need drain semantics can pass nil.
func New(version string, drainer *Drainer, deps ...Dep) *Handler {
	if drainer == nil {
		drainer = &Drainer{}
	}
	return &Handler{version: version, drainer: drainer, deps: deps, timeout: readyCheckTimeout}
}

// Live is the liveness probe: always 200 while the process serves.
func (h *Handler) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "alive", "version": h.version})
}

// Ready is the readiness probe. 200 only when active AND every dependency
// passes; 503 while draining or when any dependency is down.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	checks := make(map[string]string, len(h.deps))
	depsOK := true
	for _, d := range h.deps {
		if err := d.Check(ctx); err != nil {
			depsOK = false
			checks[d.Name] = "down: " + err.Error()
			continue
		}
		checks[d.Name] = "ok"
	}

	status, code := "ready", http.StatusOK
	switch {
	case !h.drainer.Active():
		status, code = "draining", http.StatusServiceUnavailable
	case !depsOK:
		status, code = "not_ready", http.StatusServiceUnavailable
	}

	writeJSON(w, code, map[string]any{
		"status":  status,
		"version": h.version,
		"checks":  checks,
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Warn("health: encode response", slog.String("err", err.Error()))
	}
}
