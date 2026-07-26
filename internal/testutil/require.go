package testutil

import (
	"os"
	"testing"
)

// EnvDatabaseURL is the admin DSN every real-Postgres test provisions from.
const EnvDatabaseURL = "TRACK_TEST_DATABASE_URL"

// RequireDatabaseURL returns the admin DSN, FAILING the test when it is absent.
//
// AUDIT FINDING (guard that does not run): these tests used to t.Skip when the variable
// was unset. Roughly 28% of the suite is real-Postgres-gated — including every IDOR and
// cross-tenancy integration test — so a dropped variable or a service container that
// failed to come up would take the entire tenancy suite out of the run while `go test`
// still exited 0. A skipped IDOR suite is indistinguishable from a passing one, and the
// distinction is exactly what a pre-deployment gate exists to make.
//
// So: missing database ⇒ FATAL, not skipped. CI cannot go green having tested nothing.
//
// The ONLY escape is `go test -short`, which a developer types deliberately on the
// command line. It is not an environment variable, so it cannot be inherited from a
// stale CI config or a forgotten export — the two ways this silently came back. CI never
// passes -short, and the `guards` job asserts that (see .github/workflows/ci.yaml).
func RequireDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(EnvDatabaseURL)
	if dsn != "" {
		return dsn
	}
	if testing.Short() {
		t.Skipf("%s not set and -short given — skipping real-Postgres test by explicit request", EnvDatabaseURL)
	}
	t.Fatalf(`%s is not set.

This test needs a real Postgres. It FAILS rather than skips: a silently skipped
tenancy/IDOR suite looks exactly like a passing one, which is how these gates stopped
running unnoticed.

  Start one:  docker run -d --name track-test-pg \
                -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres \
                -p 5432:5432 pgvector/pgvector:pg16
  Point at it: export %s='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable'

  Or, to run only the tests that need no database:  go test -short ./...`,
		EnvDatabaseURL, EnvDatabaseURL)
	return ""
}
