// Package db sets up the shared pgxpool for the Track server.
//
// The connection pool is created once at startup and reused by every
// store. ParseConfig honors PG-standard environment variables (sslmode,
// pool_max_conns, etc.) so deployments can tune the pool without
// changing code.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Query/connection bounds so a wedged or unreachable Postgres can't pin a
// request. statement_timeout is sent as a startup RuntimeParam so Postgres
// itself aborts any query exceeding it (SQLSTATE 57014) — every query,
// app-wide, with no per-store change. ConnectTimeout bounds dialing so
// acquiring a connection against a down DB fails fast instead of hanging.
const (
	// DefaultStatementTimeoutMS bounds a single query at 10s — comfortably below
	// the 30s HTTP request timeout so a wedged query can't consume the whole
	// request budget, yet generous enough not to clip a legitimately slow report.
	// Tune per-deployment by setting statement_timeout in the dsn.
	DefaultStatementTimeoutMS = "10000"         // 10s, expressed in milliseconds
	DefaultConnectTimeout     = 5 * time.Second // per-connection dial timeout
)

// New parses dsn, applies sensible defaults, and verifies the pool can
// reach Postgres with a ping. Returns a ready-to-use pool or an error.
func New(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse dsn: %w", err)
	}
	// Cap the pool to keep a single Track instance friendly on small
	// Postgres deployments. Override via the dsn (pool_max_conns=N) if
	// you want a larger pool.
	if cfg.MaxConns == 0 || cfg.MaxConns > 50 {
		cfg.MaxConns = 50
	}
	// Bound every query's run time and every connection's dial time so a wedged
	// or unreachable DB degrades gracefully instead of hanging requests. Both
	// respect an explicit value already supplied via the dsn.
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if _, ok := cfg.ConnConfig.RuntimeParams["statement_timeout"]; !ok {
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = DefaultStatementTimeoutMS
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = DefaultConnectTimeout
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}
