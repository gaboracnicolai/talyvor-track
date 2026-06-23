package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func dsnOrSkip(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TRACK_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TRACK_TEST_DATABASE_URL not set — skipping real-Postgres db test")
	}
	return dsn
}

func withParam(dsn, kv string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + kv
	}
	return dsn + "?" + kv
}

// TestNew_AppliesStatementTimeout proves New sets a non-zero statement_timeout
// on its connections by default, so no query can run unbounded.
func TestNew_AppliesStatementTimeout(t *testing.T) {
	dsn := dsnOrSkip(t)
	ctx := context.Background()
	pool, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer pool.Close()

	var st string
	if err := pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&st); err != nil {
		t.Fatalf("SHOW statement_timeout: %v", err)
	}
	if st == "0" || st == "" {
		t.Fatalf("statement_timeout = %q, want a non-zero default bound", st)
	}
}

// TestNew_StatementTimeoutAbortsLongQuery is the "a wedged DB can't hang a
// request" guarantee: with a short statement_timeout, a deliberately slow query
// is aborted by Postgres promptly rather than running to completion.
func TestNew_StatementTimeoutAbortsLongQuery(t *testing.T) {
	dsn := withParam(dsnOrSkip(t), "statement_timeout=300") // 300ms, overrides the default
	ctx := context.Background()
	pool, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer pool.Close()

	start := time.Now()
	_, err = pool.Exec(ctx, "SELECT pg_sleep(2)")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected pg_sleep(2) to be aborted by statement_timeout")
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("query ran %s before erroring; statement_timeout did not abort it promptly", elapsed)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "statement timeout") {
		t.Errorf("err = %v, want a statement-timeout (57014) error", err)
	}
}
