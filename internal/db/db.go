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

	"github.com/jackc/pgx/v5/pgxpool"
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
