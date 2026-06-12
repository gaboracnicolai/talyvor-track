package notification

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/talyvor/track/internal/email"
)

func newMockDLQ(t *testing.T) (*DeadLetterStore, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newDeadLetterStore(pool), pool
}

func TestDeadLetterStore_RecordInsertsMetadata(t *testing.T) {
	s, pool := newMockDLQ(t)
	// Only metadata is persisted — never the rendered body (avoids storing
	// notification content / extra PII in an ops table).
	pool.ExpectExec(`INSERT INTO notification_dead_letters`).
		WithArgs([]string{"a@b.c"}, "ENG-1 assigned", 3, "smtp down").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	err := s.Record(context.Background(),
		email.Message{To: []string{"a@b.c"}, Subject: "ENG-1 assigned", HTMLBody: "<p>secret</p>"},
		3, "smtp down")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestDeadLetterStore_ListReturnsRows(t *testing.T) {
	s, pool := newMockDLQ(t)
	when := time.Date(2026, 6, 12, 1, 0, 0, 0, time.UTC)
	pool.ExpectQuery(`SELECT .* FROM notification_dead_letters`).
		WithArgs(50).
		WillReturnRows(pgxmock.NewRows([]string{"id", "recipients", "subject", "attempts", "last_error", "created_at"}).
			AddRow(int64(7), []string{"a@b.c"}, "ENG-1 assigned", 3, "smtp down", when))

	out, err := s.List(context.Background(), 50)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 row, got %d", len(out))
	}
	got := out[0]
	if got.ID != 7 || got.Subject != "ENG-1 assigned" || got.Attempts != 3 || got.LastError != "smtp down" {
		t.Fatalf("unexpected row: %+v", got)
	}
	if len(got.Recipients) != 1 || got.Recipients[0] != "a@b.c" {
		t.Fatalf("unexpected recipients: %v", got.Recipients)
	}
}

// Compile-time assertion that the store satisfies the queue's sink interface.
var _ email.DeadLetterSink = (*DeadLetterStore)(nil)
