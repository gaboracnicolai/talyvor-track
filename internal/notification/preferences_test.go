package notification

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func newMockPrefs(t *testing.T) (*PreferenceStore, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newPreferenceStore(pool), pool
}

func TestPreferences_IsEnabledDefaultsTrueWhenNoRow(t *testing.T) {
	ps, pool := newMockPrefs(t)
	// No explicit row → opt-out model means the member is opted IN.
	pool.ExpectQuery(`SELECT email_enabled FROM notification_preferences`).
		WithArgs("m1", "issue.assigned").
		WillReturnRows(pgxmock.NewRows([]string{"email_enabled"}))

	ok, err := ps.IsEnabled(context.Background(), "m1", "issue.assigned")
	if err != nil {
		t.Fatalf("IsEnabled: %v", err)
	}
	if !ok {
		t.Fatal("missing preference row should default to enabled (true)")
	}
}

func TestPreferences_IsEnabledRespectsExplicitFalse(t *testing.T) {
	ps, pool := newMockPrefs(t)
	pool.ExpectQuery(`SELECT email_enabled FROM notification_preferences`).
		WithArgs("m1", "issue.commented").
		WillReturnRows(pgxmock.NewRows([]string{"email_enabled"}).AddRow(false))

	ok, err := ps.IsEnabled(context.Background(), "m1", "issue.commented")
	if err != nil {
		t.Fatalf("IsEnabled: %v", err)
	}
	if ok {
		t.Fatal("explicit email_enabled=false must suppress")
	}
}

func TestPreferences_EnabledMembersExcludesOptedOut(t *testing.T) {
	ps, pool := newMockPrefs(t)
	// m2 has opted out; m1 and m3 have no row (default in).
	pool.ExpectQuery(`SELECT member_id FROM notification_preferences`).
		WithArgs("issue.status_changed", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"member_id"}).AddRow("m2"))

	got, err := ps.EnabledMembers(context.Background(), "issue.status_changed", []string{"m1", "m2", "m3"})
	if err != nil {
		t.Fatalf("EnabledMembers: %v", err)
	}
	want := map[string]bool{"m1": true, "m3": true}
	if len(got) != 2 {
		t.Fatalf("got %v, want m1 and m3", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected member %q in enabled set", id)
		}
	}
}

func TestPreferences_SetUpserts(t *testing.T) {
	ps, pool := newMockPrefs(t)
	pool.ExpectExec(`INSERT INTO notification_preferences`).
		WithArgs("m1", "issue.assigned", false).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := ps.Set(context.Background(), "m1", "issue.assigned", false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}
