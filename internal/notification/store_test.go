package notification

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newStore(pool), pool
}

func notifRow(id, memberID, title string, read bool) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "member_id", "type", "title", "body", "issue_id", "read", "created_at",
	}).AddRow(id, "ws-1", memberID, "mention", title, "", nil, read, time.Now().UTC())
}

func TestCreate_InsertsNotification(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`INSERT INTO notifications`).
		WithArgs("ws-1", "alice", "mention", "Alice mentioned you", "", pgxmock.AnyArg()).
		WillReturnRows(notifRow("n-1", "alice", "Alice mentioned you", false))

	out, err := store.Create(context.Background(), Notification{
		WorkspaceID: "ws-1", MemberID: "alice", Type: "mention",
		Title: "Alice mentioned you",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Read {
		t.Errorf("new notifications must start unread; got read=true")
	}
}

func TestCreate_RejectsMissingFields(t *testing.T) {
	store, _ := newMockStore(t)
	if _, err := store.Create(context.Background(), Notification{Title: "x"}); err == nil {
		t.Error("expected error on missing fields")
	}
}

func TestList_ReturnsUnreadFirst(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`FROM notifications\s+WHERE member_id = \$1\s+ORDER BY read ASC, created_at DESC`).
		WithArgs("alice", 50).
		WillReturnRows(notifRow("n-1", "alice", "unread one", false).
			AddRow("n-2", "ws-1", "alice", "mention", "unread two", "", nil, false, time.Now().UTC()).
			AddRow("n-3", "ws-1", "alice", "mention", "read one", "", nil, true, time.Now().UTC()))

	out, err := store.List(context.Background(), "alice", false, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d, want 3", len(out))
	}
	if out[0].Read || out[1].Read {
		t.Errorf("unread should come first; got %+v / %+v", out[0], out[1])
	}
	if !out[2].Read {
		t.Errorf("last should be read; got %+v", out[2])
	}
}

func TestList_UnreadOnlyFiltersOut(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`AND read = false`).
		WithArgs("alice", 50).
		WillReturnRows(notifRow("n-1", "alice", "unread", false))

	out, err := store.List(context.Background(), "alice", true, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].Read {
		t.Errorf("unread-only filter broken: %+v", out)
	}
}

func TestMarkRead_UpdatesSingle(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`UPDATE notifications SET read = true WHERE id`).
		WithArgs("n-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := store.MarkRead(context.Background(), "n-1"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
}

func TestMarkAllRead_ClearsForMember(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`UPDATE notifications SET read = true WHERE member_id`).
		WithArgs("alice").
		WillReturnResult(pgxmock.NewResult("UPDATE", 7))

	if err := store.MarkAllRead(context.Background(), "alice"); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
}
