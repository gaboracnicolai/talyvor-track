package workflow

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

func newMockEngine(t *testing.T) (*Engine, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newEngine(pool), pool
}

func statusRows() *pgxmock.Rows {
	r := pgxmock.NewRows([]string{"id", "team_id", "name", "color", "category", "position", "is_default"})
	r.AddRow("s1", "team-1", "Backlog", "#94a3b8", "backlog", 0, true)
	r.AddRow("s2", "team-1", "Todo", "#94a3b8", "unstarted", 1, true)
	r.AddRow("s3", "team-1", "In Progress", "#3b82f6", "started", 2, true)
	r.AddRow("s4", "team-1", "In Review", "#f59e0b", "started", 3, true)
	r.AddRow("s5", "team-1", "Done", "#10b981", "completed", 4, true)
	r.AddRow("s6", "team-1", "Cancelled", "#ef4444", "cancelled", 5, true)
	return r
}

func TestGetStatuses_ReturnsDefaultsForTeam(t *testing.T) {
	engine, pool := newMockEngine(t)
	pool.ExpectQuery(`FROM workflow_statuses WHERE team_id`).
		WithArgs("team-1").
		WillReturnRows(statusRows())

	out, err := engine.GetStatuses(context.Background(), "team-1")
	if err != nil {
		t.Fatalf("GetStatuses: %v", err)
	}
	if len(out) != 6 {
		t.Fatalf("got %d, want 6", len(out))
	}
	if out[0].Name != "Backlog" || out[5].Name != "Cancelled" {
		t.Errorf("ordering off: %+v", out)
	}
}

func TestGetStatuses_CachesAfterFirstFetch(t *testing.T) {
	engine, pool := newMockEngine(t)
	pool.ExpectQuery(`FROM workflow_statuses`).
		WithArgs("team-1").
		WillReturnRows(statusRows())

	if _, err := engine.GetStatuses(context.Background(), "team-1"); err != nil {
		t.Fatalf("first GetStatuses: %v", err)
	}
	// Second call must NOT hit the DB — only one Expect set up.
	if _, err := engine.GetStatuses(context.Background(), "team-1"); err != nil {
		t.Fatalf("cached GetStatuses: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB calls on cached read: %v", err)
	}
}

func TestSeedDefaults_CreatesSixStatuses(t *testing.T) {
	engine, pool := newMockEngine(t)
	// Six INSERT ... ON CONFLICT DO NOTHING calls expected.
	for i := 0; i < 6; i++ {
		pool.ExpectExec(`INSERT INTO workflow_statuses`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
	}

	if err := engine.SeedDefaults(context.Background(), "team-1"); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("SeedDefaults did not issue all 6 inserts: %v", err)
	}
}

func TestCreateStatus_AddsToTeam(t *testing.T) {
	engine, pool := newMockEngine(t)
	pool.ExpectQuery(`INSERT INTO workflow_statuses`).
		WithArgs("team-1", "Blocked", "#ef4444", "unstarted", 99, false, "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "team_id", "name", "color", "category", "position", "is_default",
		}).AddRow("s-new", "team-1", "Blocked", "#ef4444", "unstarted", 99, false))

	out, err := engine.CreateStatus(context.Background(), WorkflowStatus{
		TeamID: "team-1", Name: "Blocked", Color: "#ef4444",
		Category: CategoryUnstarted, Position: 99,
	}, "ws-1")
	if err != nil {
		t.Fatalf("CreateStatus: %v", err)
	}
	if out.Name != "Blocked" {
		t.Errorf("name = %q", out.Name)
	}
}

func TestDeleteStatus_FailsWithActiveIssues(t *testing.T) {
	engine, pool := newMockEngine(t)
	pool.ExpectQuery(`SELECT team_id, name FROM workflow_statuses`).
		WithArgs("s-x", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"team_id", "name"}).AddRow("team-1", "Blocked"))
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issues`).
		WithArgs("team-1", "Blocked").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(3)))

	err := engine.DeleteStatus(context.Background(), "s-x", "ws-1")
	if err == nil {
		t.Fatal("DeleteStatus should error when active issues exist")
	}
}

func TestValidateTransition_BlocksCompletedToBacklog(t *testing.T) {
	engine, _ := newMockEngine(t)
	err := engine.ValidateTransition(
		WorkflowStatus{Name: "Done", Category: CategoryCompleted},
		WorkflowStatus{Name: "Backlog", Category: CategoryBacklog},
	)
	if err == nil {
		t.Error("completed → backlog should produce a warning")
	}
}

func TestValidateTransition_BlocksCancelledToStarted(t *testing.T) {
	engine, _ := newMockEngine(t)
	err := engine.ValidateTransition(
		WorkflowStatus{Name: "Cancelled", Category: CategoryCancelled},
		WorkflowStatus{Name: "In Progress", Category: CategoryStarted},
	)
	if err == nil {
		t.Error("cancelled → started should produce a warning")
	}
}

func TestValidateTransition_AllowsTodoToInProgress(t *testing.T) {
	engine, _ := newMockEngine(t)
	err := engine.ValidateTransition(
		WorkflowStatus{Name: "Todo", Category: CategoryUnstarted},
		WorkflowStatus{Name: "In Progress", Category: CategoryStarted},
	)
	if err != nil {
		t.Errorf("normal transition should be allowed; got %v", err)
	}
}
