package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/testutil"
)

// TestReadyz_RealPostgres_Up exercises the DB-aware readiness path against a
// real, fully-migrated Postgres (via the internal/testutil harness): with the
// pool reachable, /readyz reports 200 and the database check is "ok". Skips
// cleanly when TRACK_TEST_DATABASE_URL is unset.
func TestReadyz_RealPostgres_Up(t *testing.T) {
	db := testutil.New(t) // SKIPs without TRACK_TEST_DATABASE_URL

	h := New("pg-test", &Drainer{}, PingDep("database", db.Pool))
	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d, want 200 with real Postgres up; body=%s", rec.Code, rec.Body.String())
	}
	checks, _ := decode(t, rec)["checks"].(map[string]any)
	if checks["database"] != "ok" {
		t.Errorf("checks[database] = %v, want ok", checks["database"])
	}
}

// TestReadyz_PostgresDown_503 is the load-balancer-drain guarantee with a real
// pgx pool: when Postgres is unreachable, Ping fails and /readyz reports 503 so
// the LB pulls this instance out of rotation instead of routing traffic to a
// broken one. Self-contained (no live DB needed — the pool points at an
// unreachable endpoint), so it runs everywhere.
func TestReadyz_PostgresDown_503(t *testing.T) {
	h := New("pg-test", &Drainer{}, PingDep("database", unreachablePool(t)))
	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503 when Postgres is unreachable; body=%s", rec.Code, rec.Body.String())
	}
	checks, _ := decode(t, rec)["checks"].(map[string]any)
	if s, _ := checks["database"].(string); len(s) < 5 || s[:5] != "down:" {
		t.Errorf("checks[database] = %q, want a \"down: ...\" message", s)
	}
}

// unreachablePool returns a real *pgxpool.Pool whose connections cannot be
// established (nothing listens on 127.0.0.1:1). NewWithConfig is lazy, so the
// failure surfaces at Ping time — exactly what the readiness check observes
// during a Postgres outage.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatalf("parse unreachable dsn: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("build unreachable pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
