package cycle

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/talyvor/track/internal/model"
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

func cycleRow(id string, number int, status string, start, end time.Time) *pgxmock.Rows {
	now := time.Now().UTC()
	return pgxmock.NewRows([]string{
		"id", "team_id", "workspace_id", "name", "number", "status",
		"start_date", "end_date", "created_at", "updated_at",
	}).AddRow(id, "team-1", "ws-1", "Sprint", number, status, start, end, now, now)
}

func TestCreate_AutoNumbersCyclesPerTeam(t *testing.T) {
	store, pool := newMockStore(t)
	start := time.Now().UTC()
	end := start.Add(14 * 24 * time.Hour)

	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`SELECT COALESCE\(MAX\(number\), 0\) \+ 1 FROM cycles`).
		WithArgs("team-1").
		WillReturnRows(pgxmock.NewRows([]string{"next"}).AddRow(3))
	pool.ExpectQuery(`INSERT INTO cycles`).
		WithArgs("team-1", "ws-1", "Sprint 3", 3, "upcoming", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(cycleRow("c-3", 3, "upcoming", start, end))

	out, err := store.Create(context.Background(), model.Cycle{
		WorkspaceID: "ws-1", TeamID: "team-1", Name: "Sprint 3",
		StartDate: start, EndDate: end,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Number != 3 {
		t.Errorf("Number = %d, want 3", out.Number)
	}
}

func TestCreate_RejectsEndBeforeStart(t *testing.T) {
	store, _ := newMockStore(t)
	start := time.Now().UTC()
	bad := start.Add(-24 * time.Hour)

	_, err := store.Create(context.Background(), model.Cycle{
		WorkspaceID: "ws-1", TeamID: "team-1", Name: "Bad",
		StartDate: start, EndDate: bad,
	})
	if err == nil {
		t.Error("Create should reject EndDate before StartDate")
	}
}

func TestGetActive_ReturnsCurrentCycle(t *testing.T) {
	store, pool := newMockStore(t)
	start := time.Now().UTC().Add(-7 * 24 * time.Hour)
	end := time.Now().UTC().Add(7 * 24 * time.Hour)

	pool.ExpectQuery(`FROM cycles\s+WHERE team_id = \$1 AND status = 'active'`).
		WithArgs("team-1").
		WillReturnRows(cycleRow("c-active", 5, "active", start, end))

	out, err := store.GetActive(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if out == nil || out.Status != "active" {
		t.Errorf("expected active cycle; got %+v", out)
	}
}

func TestGetProgress_CalculatesCompletionCorrectly(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`COUNT.*FILTER.*FROM issues WHERE cycle_id`).
		WithArgs("c-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"total", "completed", "in_progress", "not_started", "total_ai_cost",
		}).AddRow(10, 4, 3, 3, 0.85))

	p, err := store.GetProgress(context.Background(), "c-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if p.TotalIssues != 10 || p.Completed != 4 {
		t.Errorf("counts wrong: %+v", p)
	}
	if p.CompletionPct < 0.39 || p.CompletionPct > 0.41 {
		t.Errorf("CompletionPct = %v, want ~0.40", p.CompletionPct)
	}
	if p.TotalAICostUSD != 0.85 {
		t.Errorf("TotalAICostUSD = %v, want 0.85", p.TotalAICostUSD)
	}
	if p.AvgAICostPerIssue < 0.084 || p.AvgAICostPerIssue > 0.086 {
		t.Errorf("AvgAICostPerIssue = %v, want ~0.085", p.AvgAICostPerIssue)
	}
}

func TestGetBurndown_ReturnsCorrectDataPoints(t *testing.T) {
	store, pool := newMockStore(t)
	// Cycle spans 5 days; total issues = 10.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(4 * 24 * time.Hour) // 5-day inclusive window

	pool.ExpectQuery(`FROM cycles WHERE id`).
		WithArgs("c-1").
		WillReturnRows(cycleRow("c-1", 1, "active", start, end))
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issues WHERE cycle_id`).
		WithArgs("c-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(10))
	// One COUNT query per day. We supply a steady completion rate so
	// remaining decreases predictably.
	for i := 0; i < 5; i++ {
		pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issues\s+WHERE cycle_id = \$1 AND completed_at IS NOT NULL`).
			WithArgs("c-1", pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(i * 2))
	}

	out, err := store.GetBurndown(context.Background(), "c-1")
	if err != nil {
		t.Fatalf("GetBurndown: %v", err)
	}
	if len(out) != 5 {
		t.Fatalf("got %d points, want 5", len(out))
	}
	// First point: 10 issues remaining; ideal = 10. Last point:
	// remaining = 10 - 8 = 2; ideal = 0.
	if out[0].Remaining != 10 || out[0].Ideal != 10 {
		t.Errorf("first point = %+v, want remaining=10, ideal=10", out[0])
	}
	if out[4].Ideal != 0 {
		t.Errorf("last point ideal = %d, want 0", out[4].Ideal)
	}
}

func TestComplete_DetachesIncompleteIssues(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`UPDATE cycles SET status = 'completed'`).
		WithArgs("c-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE issues SET cycle_id = NULL`).
		WithArgs("c-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	if err := store.Complete(context.Background(), "c-1", "ws-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}
