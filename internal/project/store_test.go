package project

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

func projRow(id, identifier, status string) *pgxmock.Rows {
	now := time.Now().UTC()
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "team_id", "name", "identifier", "description", "status",
		"priority", "start_date", "target_date", "created_at", "updated_at",
	}).AddRow(id, "ws-1", "team-1", "Project", identifier, "", status, 0, nil, nil, now, now)
}

func TestCreate_InsertsProject(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`INSERT INTO projects`).
		WithArgs("ws-1", "team-1", "Project", "PRJ-1", "", "active", 0,
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(projRow("p-1", "PRJ-1", "active"))

	out, err := store.Create(context.Background(), model.Project{
		WorkspaceID: "ws-1", TeamID: "team-1", Name: "Project", Identifier: "PRJ-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Status != "active" {
		t.Errorf("status default not applied: %q", out.Status)
	}
}

func TestCreate_RejectsMissingFields(t *testing.T) {
	store, _ := newMockStore(t)
	if _, err := store.Create(context.Background(), model.Project{Name: "x"}); err == nil {
		t.Error("expected error on missing fields")
	}
}

func TestListByWorkspace_ReturnsProjects(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`FROM projects WHERE workspace_id`).
		WithArgs("ws-1").
		WillReturnRows(projRow("a", "PRJ-1", "active").AddRow(
			"b", "ws-1", "team-1", "Other", "PRJ-2", "", "planned",
			0, nil, nil, time.Now().UTC(), time.Now().UTC(),
		))

	out, err := store.ListByWorkspace(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ListByWorkspace: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("got %d, want 2", len(out))
	}
}

func TestUpdate_ChangesStatus(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`UPDATE projects SET`).
		WithArgs("completed", "p-1").
		WillReturnRows(projRow("p-1", "PRJ-1", "completed"))

	out, err := store.Update(context.Background(), "p-1", map[string]any{"status": "completed"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.Status != "completed" {
		t.Errorf("status = %q, want completed", out.Status)
	}
}

func TestDelete_RemovesProject(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`DELETE FROM projects`).
		WithArgs("p-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := store.Delete(context.Background(), "p-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
