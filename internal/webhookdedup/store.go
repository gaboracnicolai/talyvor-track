// Package webhookdedup is the durable cross-delivery replay guard for inbound webhooks (SEC-7). It claims
// a provider delivery id exactly once so a retried or replayed delivery re-runs no side effects. Shared by
// the GitHub automation handler (X-GitHub-Delivery) and available to the Lens webhook (a future Lens-sent
// event_id — see webhook.go TODO).
package webhookdedup

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Store struct{ pool pgxDB }

func New(pool *pgxpool.Pool) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return &Store{pool: db}
}

// Claim inserts (source, deliveryID) and reports whether THIS call won the insert. true = first delivery
// (proceed); false = a repeat (caller must no-op). A nil pool claims nothing (returns true) so a
// misconfigured deployment fails OPEN rather than dropping every delivery.
func (s *Store) Claim(ctx context.Context, source, deliveryID string) (bool, error) {
	if s.pool == nil {
		return true, nil
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO webhook_deliveries (source, delivery_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		source, deliveryID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Prune deletes delivery rows older than the retention window (call periodically). Bounds the table while
// keeping the replay window generously wider than any provider's retry horizon.
func (s *Store) Prune(ctx context.Context, olderThan time.Duration) error {
	if s.pool == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM webhook_deliveries WHERE received_at < now() - $1::interval`,
		olderThan.String())
	return err
}
