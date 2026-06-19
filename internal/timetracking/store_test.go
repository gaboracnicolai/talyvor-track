package timetracking

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

func ptrTime(t time.Time) *time.Time { return &t }

// Common row template for TimeEntry results.
func entryRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "issue_id", "workspace_id", "member_id", "description",
		"started_at", "stopped_at", "duration_sec", "billable", "created_at",
	})
}

// ─── StartTimer ────────────────────────────────────────────

func TestStartTimer_CreatesRunningEntry(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// Object-graph guard: the issue belongs to the workspace.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("i-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectBegin()
	// Stop any running timer for this member (no-op if none).
	pool.ExpectExec(`UPDATE time_entries SET stopped_at`).
		WithArgs("m-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	// Insert the new running entry.
	pool.ExpectQuery(`INSERT INTO time_entries`).
		WithArgs("i-1", "ws-1", "m-1", "writing").
		WillReturnRows(entryRows().AddRow(
			"t-1", "i-1", "ws-1", "m-1", "writing",
			now, (*time.Time)(nil), 0, true, now,
		))
	pool.ExpectCommit()

	out, err := store.StartTimer(context.Background(), "i-1", "ws-1", "m-1", "writing")
	if err != nil {
		t.Fatalf("StartTimer: %v", err)
	}
	if out.ID != "t-1" || out.StoppedAt != nil {
		t.Errorf("got %+v", out)
	}
}

func TestStartTimer_StopsPreviousRunningTimer(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("i-2", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectBegin()
	// The "stop previous" UPDATE returns RowsAffected=1 — the
	// previous running timer was closed atomically.
	pool.ExpectExec(`UPDATE time_entries SET stopped_at`).
		WithArgs("m-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectQuery(`INSERT INTO time_entries`).
		WithArgs("i-2", "ws-1", "m-1", "next task").
		WillReturnRows(entryRows().AddRow(
			"t-2", "i-2", "ws-1", "m-1", "next task",
			now, (*time.Time)(nil), 0, true, now,
		))
	pool.ExpectCommit()

	out, err := store.StartTimer(context.Background(), "i-2", "ws-1", "m-1", "next task")
	if err != nil {
		t.Fatalf("StartTimer: %v", err)
	}
	if out.IssueID != "i-2" {
		t.Errorf("issue_id = %v", out.IssueID)
	}
}

// ─── StopTimer ─────────────────────────────────────────────

func TestStopTimer_CompletesEntryWithDuration(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`UPDATE time_entries SET stopped_at`).
		WithArgs("m-1", "ws-1").
		WillReturnRows(entryRows().AddRow(
			"t-1", "i-1", "ws-1", "m-1", "writing",
			now.Add(-90*time.Second), &now, 90, true, now.Add(-90*time.Second),
		))

	out, err := store.StopTimer(context.Background(), "m-1", "ws-1")
	if err != nil {
		t.Fatalf("StopTimer: %v", err)
	}
	if out.DurationSec != 90 {
		t.Errorf("duration = %d, want 90", out.DurationSec)
	}
	if out.StoppedAt == nil {
		t.Errorf("stopped_at should be set")
	}
}

func TestStopTimer_NoRunningEntryReturnsNil(t *testing.T) {
	store, pool := newMockStore(t)
	// UPDATE ... RETURNING with no matching row → pgx.ErrNoRows.
	pool.ExpectQuery(`UPDATE time_entries SET stopped_at`).
		WithArgs("m-1", "ws-1").
		WillReturnRows(entryRows())

	out, err := store.StopTimer(context.Background(), "m-1", "ws-1")
	if err != nil {
		t.Fatalf("StopTimer with no running entry should be nil-nil, got err: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil, got %+v", out)
	}
}

// ─── GetRunningTimer ───────────────────────────────────────

func TestGetRunningTimer_ReturnsElapsedCorrectly(t *testing.T) {
	store, pool := newMockStore(t)
	startedAt := time.Now().UTC().Add(-2 * time.Minute) // 120s elapsed
	pool.ExpectQuery(`SELECT issue_id, started_at FROM time_entries`).
		WithArgs("m-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"issue_id", "started_at"}).
			AddRow("i-1", startedAt))

	out, err := store.GetRunningTimer(context.Background(), "m-1", "ws-1")
	if err != nil {
		t.Fatalf("GetRunningTimer: %v", err)
	}
	if !out.Running {
		t.Error("Running should be true")
	}
	// Some wall-clock jitter between the test setup and the
	// GetRunningTimer call is expected; tolerate ±5s.
	if out.ElapsedSec < 115 || out.ElapsedSec > 125 {
		t.Errorf("elapsed = %d, want ~120", out.ElapsedSec)
	}
}

func TestGetRunningTimer_ReturnsNotRunningWhenNone(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT issue_id, started_at FROM time_entries`).
		WithArgs("m-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"issue_id", "started_at"}))

	out, err := store.GetRunningTimer(context.Background(), "m-1", "ws-1")
	if err != nil {
		t.Fatalf("GetRunningTimer: %v", err)
	}
	if out.Running {
		t.Error("Running should be false")
	}
}

// ─── LogTime ───────────────────────────────────────────────

func TestLogTime_CreatesManualEntry(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	start := now.Add(-30 * time.Minute)
	stop := now
	// 30 min = 1800 sec; the store computes duration from the
	// timestamps so the caller doesn't have to.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("i-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`INSERT INTO time_entries`).
		WithArgs("i-1", "ws-1", "m-1", "did a thing",
			start, ptrTime(stop), 1800, true).
		WillReturnRows(entryRows().AddRow(
			"t-9", "i-1", "ws-1", "m-1", "did a thing",
			start, &stop, 1800, true, now,
		))

	out, err := store.LogTime(context.Background(), TimeEntry{
		IssueID:     "i-1",
		WorkspaceID: "ws-1",
		MemberID:    "m-1",
		Description: "did a thing",
		StartedAt:   start,
		StoppedAt:   &stop,
		Billable:    true,
	})
	if err != nil {
		t.Fatalf("LogTime: %v", err)
	}
	if out.DurationSec != 1800 {
		t.Errorf("duration = %d, want 1800", out.DurationSec)
	}
}

func TestLogTime_RejectsMissingStoppedAt(t *testing.T) {
	store, _ := newMockStore(t)
	if _, err := store.LogTime(context.Background(), TimeEntry{
		IssueID:     "i-1",
		WorkspaceID: "ws-1",
		MemberID:    "m-1",
		StartedAt:   time.Now(),
	}); err == nil {
		t.Fatal("expected error: manual log without stopped_at")
	}
}

// ─── ListByIssue ───────────────────────────────────────────

func TestListByIssue_ReturnsEntries(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT .* FROM time_entries WHERE issue_id`).
		WithArgs("i-1").
		WillReturnRows(entryRows().
			AddRow("t-1", "i-1", "ws", "m-1", "a", now.Add(-time.Hour), &now, 3600, true, now).
			AddRow("t-2", "i-1", "ws", "m-2", "b", now.Add(-time.Hour), &now, 1200, false, now))

	out, err := store.ListByIssue(context.Background(), "i-1")
	if err != nil {
		t.Fatalf("ListByIssue: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
}

// ─── GetIssueSummary ───────────────────────────────────────

func TestGetIssueSummary_SumsBillableAndTotal(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT .* FROM time_entries WHERE issue_id = \$1`).
		WithArgs("i-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"total_sec", "billable_sec", "member_count", "entry_count",
		}).AddRow(int64(7200), int64(5400), int64(2), int64(3)))

	out, err := store.GetIssueSummary(context.Background(), "i-1")
	if err != nil {
		t.Fatalf("GetIssueSummary: %v", err)
	}
	if out.TotalSec != 7200 || out.BillableSec != 5400 {
		t.Errorf("got %+v", out)
	}
	if out.MemberCount != 2 || out.EntryCount != 3 {
		t.Errorf("counts wrong: %+v", out)
	}
}

// ─── GetWorkspaceSummary ───────────────────────────────────

func TestGetWorkspaceSummary_GroupsByMember(t *testing.T) {
	store, pool := newMockStore(t)
	since := time.Now().UTC().AddDate(0, 0, -7)

	// Totals (one row).
	pool.ExpectQuery(`SELECT COALESCE\(SUM\(duration_sec\)`).
		WithArgs("ws-1", since).
		WillReturnRows(pgxmock.NewRows([]string{"total_sec", "billable_sec"}).
			AddRow(int64(36000), int64(27000)))

	// By member.
	pool.ExpectQuery(`JOIN members`).
		WithArgs("ws-1", since).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "total_sec", "billable_sec"}).
			AddRow("m-1", "Alice", int64(24000), int64(18000)).
			AddRow("m-2", "Bob", int64(12000), int64(9000)))

	// By project.
	pool.ExpectQuery(`JOIN issues`).
		WithArgs("ws-1", since).
		WillReturnRows(pgxmock.NewRows([]string{"project_id", "name", "total_sec", "billable_sec"}).
			AddRow("p-1", "Launch", int64(36000), int64(27000)))

	out, err := store.GetWorkspaceSummary(context.Background(), "ws-1", since)
	if err != nil {
		t.Fatalf("GetWorkspaceSummary: %v", err)
	}
	if out.TotalSec != 36000 {
		t.Errorf("TotalSec = %d, want 36000", out.TotalSec)
	}
	if len(out.ByMember) != 2 {
		t.Errorf("ByMember = %d, want 2", len(out.ByMember))
	}
	if out.ByMember[0].Name != "Alice" || out.ByMember[0].TotalSec != 24000 {
		t.Errorf("ByMember[0] = %+v", out.ByMember[0])
	}
	if len(out.ByProject) != 1 || out.ByProject[0].Name != "Launch" {
		t.Errorf("ByProject = %+v", out.ByProject)
	}
}
