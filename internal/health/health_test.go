package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func okDep(name string) Dep {
	return Dep{Name: name, Check: func(context.Context) error { return nil }}
}

func downDep(name string, err error) Dep {
	return Dep{Name: name, Check: func(context.Context) error { return err }}
}

// Liveness must be independent of dependencies: a process that is running is
// alive, full stop — otherwise a DB/Redis blip would make an orchestrator kill
// an otherwise-healthy instance.
func TestLive_Always200(t *testing.T) {
	h := New("v-test", nil, downDep("database", errors.New("db is down")))
	rec := httptest.NewRecorder()
	h.Live(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("Live code = %d, want 200 even with a dependency down", rec.Code)
	}
	if got := decode(t, rec)["status"]; got != "alive" {
		t.Errorf("status = %v, want alive", got)
	}
}

func TestReady_NoDeps_Ready(t *testing.T) {
	h := New("v-test", &Drainer{})
	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("Ready code = %d, want 200", rec.Code)
	}
	if got := decode(t, rec)["status"]; got != "ready" {
		t.Errorf("status = %v, want ready", got)
	}
}

func TestReady_DepUp_200(t *testing.T) {
	h := New("v-test", &Drainer{}, okDep("database"))
	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("Ready code = %d, want 200", rec.Code)
	}
	checks, _ := decode(t, rec)["checks"].(map[string]any)
	if checks["database"] != "ok" {
		t.Errorf("checks[database] = %v, want ok", checks["database"])
	}
}

// The headline T14 guarantee: when a dependency (Postgres) is down, /readyz
// reports 503 so a load balancer drains this instance instead of routing
// traffic to a broken one.
func TestReady_DepDown_503(t *testing.T) {
	h := New("v-test", &Drainer{}, downDep("database", errors.New("connection refused")))
	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Ready code = %d, want 503 when a dependency is down", rec.Code)
	}
	body := decode(t, rec)
	if body["status"] != "not_ready" {
		t.Errorf("status = %v, want not_ready", body["status"])
	}
	checks, _ := body["checks"].(map[string]any)
	if s, _ := checks["database"].(string); s == "ok" || s == "" {
		t.Errorf("checks[database] = %q, want a down: message", s)
	}
}

// A draining instance is 503 on /readyz even when every dependency is healthy —
// that 503 is exactly what makes graceful drain possible.
func TestReady_Draining_503(t *testing.T) {
	d := &Drainer{}
	d.SetDraining()
	h := New("v-test", d, okDep("database"))
	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Ready code = %d, want 503 while draining", rec.Code)
	}
	if got := decode(t, rec)["status"]; got != "draining" {
		t.Errorf("status = %v, want draining", got)
	}
}

func TestDrainer_ActiveByDefault(t *testing.T) {
	if !(&Drainer{}).Active() {
		t.Fatal("a fresh Drainer must be Active (ready to serve)")
	}
}

// Drain must flip to draining BEFORE running the shutdown function, so /readyz
// is already returning 503 by the time in-flight requests are being drained.
func TestDrain_FlipsDrainingThenRunsShutdown(t *testing.T) {
	d := &Drainer{}
	var called, activeDuringShutdown bool
	err := d.Drain(context.Background(), 0, func(context.Context) error {
		called = true
		activeDuringShutdown = d.Active()
		return nil
	})
	if err != nil {
		t.Fatalf("Drain returned %v", err)
	}
	if !called {
		t.Fatal("Drain did not invoke the shutdown function")
	}
	if activeDuringShutdown {
		t.Error("instance was still Active during shutdown — drain must flip status first")
	}
}

func TestDrain_ReturnsShutdownError(t *testing.T) {
	d := &Drainer{}
	want := errors.New("shutdown failed")
	got := d.Drain(context.Background(), 0, func(context.Context) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("Drain err = %v, want %v", got, want)
	}
}

func TestPingDep_NilPinger_Down(t *testing.T) {
	dep := PingDep("database", nil)
	if dep.Name != "database" {
		t.Errorf("Name = %q, want database", dep.Name)
	}
	if dep.Check(context.Background()) == nil {
		t.Error("a nil pinger must report a failed check")
	}
}

func TestPingDep_OK(t *testing.T) {
	dep := PingDep("database", fakePinger{err: nil})
	if err := dep.Check(context.Background()); err != nil {
		t.Errorf("Check = %v, want nil", err)
	}
}

func TestPingDep_Error(t *testing.T) {
	want := errors.New("conn refused")
	dep := PingDep("database", fakePinger{err: want})
	if err := dep.Check(context.Background()); !errors.Is(err, want) {
		t.Errorf("Check = %v, want %v", err, want)
	}
}
