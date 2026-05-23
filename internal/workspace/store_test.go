package workspace

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

func wsRow(id, slug, plan string) *pgxmock.Rows {
	now := time.Now().UTC()
	return pgxmock.NewRows([]string{
		"id", "name", "slug", "logo_url", "plan", "created_at", "updated_at",
	}).AddRow(id, "Workspace", slug, "", plan, now, now)
}

func TestCreate_InsertsWorkspace(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`INSERT INTO workspaces`).
		WithArgs("Workspace", "ws", "", "free").
		WillReturnRows(wsRow("ws-1", "ws", "free"))

	out, err := store.Create(context.Background(), model.Workspace{Name: "Workspace", Slug: "ws"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.ID != "ws-1" || out.Plan != "free" {
		t.Errorf("got %+v", out)
	}
}

func TestCreate_RejectsMissingName(t *testing.T) {
	store, _ := newMockStore(t)
	if _, err := store.Create(context.Background(), model.Workspace{Slug: "ws"}); err == nil {
		t.Error("expected error on missing name")
	}
}

func TestGetBySlug_ReturnsWorkspace(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`FROM workspaces WHERE slug`).
		WithArgs("acme").
		WillReturnRows(wsRow("ws-2", "acme", "pro"))

	out, err := store.GetBySlug(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if out.Slug != "acme" || out.Plan != "pro" {
		t.Errorf("got %+v", out)
	}
}

func TestList_ReturnsAllWorkspaces(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM workspaces ORDER BY created_at DESC`).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "name", "slug", "logo_url", "plan", "created_at", "updated_at",
		}).AddRow("a", "A", "a", "", "free", now, now).
			AddRow("b", "B", "b", "", "pro", now, now))

	out, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("got %d, want 2", len(out))
	}
}

func TestUpdate_ChangesPlan(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`UPDATE workspaces SET`).
		WithArgs("enterprise", "ws-1").
		WillReturnRows(wsRow("ws-1", "ws", "enterprise"))

	out, err := store.Update(context.Background(), "ws-1", map[string]any{"plan": "enterprise"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.Plan != "enterprise" {
		t.Errorf("plan = %q, want enterprise", out.Plan)
	}
}

func TestDelete_RemovesWorkspace(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`DELETE FROM workspaces`).
		WithArgs("ws-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := store.Delete(context.Background(), "ws-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
