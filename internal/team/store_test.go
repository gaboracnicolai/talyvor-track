package team

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

func teamRow(id, identifier string) *pgxmock.Rows {
	now := time.Now().UTC()
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "name", "identifier", "color", "icon", "created_at", "updated_at",
	}).AddRow(id, "ws-1", "Engineering", identifier, "#6366f1", "", now, now)
}

func TestCreate_InsertsTeam(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`INSERT INTO teams`).
		WithArgs("ws-1", "Engineering", "ENG", "#6366f1", "").
		WillReturnRows(teamRow("team-1", "ENG"))

	out, err := store.Create(context.Background(), model.Team{
		WorkspaceID: "ws-1", Name: "Engineering", Identifier: "ENG",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Identifier != "ENG" {
		t.Errorf("identifier = %q, want ENG", out.Identifier)
	}
	if out.Color != "#6366f1" {
		t.Errorf("default color not applied: %q", out.Color)
	}
}

func TestCreate_RejectsMissingFields(t *testing.T) {
	store, _ := newMockStore(t)
	if _, err := store.Create(context.Background(), model.Team{Name: "x"}); err == nil {
		t.Error("expected error on missing fields")
	}
}

func TestListByWorkspace_ReturnsTeams(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`FROM teams WHERE workspace_id`).
		WithArgs("ws-1").
		WillReturnRows(teamRow("a", "ENG").AddRow(
			"b", "ws-1", "Marketing", "MKT", "#000000", "", time.Now().UTC(), time.Now().UTC(),
		))

	out, err := store.ListByWorkspace(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("ListByWorkspace: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("got %d, want 2", len(out))
	}
}

func TestUpdate_ChangesName(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`UPDATE teams SET`).
		WithArgs("New Name", "team-1", "ws-1").
		WillReturnRows(teamRow("team-1", "ENG"))

	if _, err := store.Update(context.Background(), "team-1", "ws-1", map[string]any{"name": "New Name"}); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func TestDelete_RemovesTeam(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`DELETE FROM teams`).
		WithArgs("team-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := store.Delete(context.Background(), "team-1", "ws-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
