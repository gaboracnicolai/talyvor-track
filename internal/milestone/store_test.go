package milestone

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

func milestoneRow(id, name, status string) *pgxmock.Rows {
	now := time.Now().UTC()
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "project_id", "name", "description", "status",
		"target_date", "completed_at", "created_at", "updated_at",
	}).AddRow(id, "ws-1", "p-1", name, "", status, nil, nil, now, now)
}

func TestCreate_MilestoneForProject(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("p-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`INSERT INTO milestones`).
		WithArgs("ws-1", "p-1", "v1.0", "", "upcoming", pgxmock.AnyArg()).
		WillReturnRows(milestoneRow("m-1", "v1.0", "upcoming"))

	out, err := store.Create(context.Background(), Milestone{
		WorkspaceID: "ws-1", ProjectID: "p-1", Name: "v1.0",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Status != "upcoming" {
		t.Errorf("default status not applied: %q", out.Status)
	}
}

func TestCreate_RejectsMissingFields(t *testing.T) {
	store, _ := newMockStore(t)
	if _, err := store.Create(context.Background(), Milestone{Name: "x"}); err == nil {
		t.Error("expected error on missing fields")
	}
}

func TestListByProject_ReturnsMilestones(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`FROM milestones WHERE project_id`).
		WithArgs("p-1", "ws-1").
		WillReturnRows(milestoneRow("a", "v1.0", "upcoming").AddRow(
			"b", "ws-1", "p-1", "v1.1", "", "upcoming", nil, nil, time.Now().UTC(), time.Now().UTC(),
		))

	out, err := store.ListByProject(context.Background(), "p-1", "ws-1")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("got %d, want 2", len(out))
	}
}

func TestGetProgress_CountsIssuesCorrectly(t *testing.T) {
	store, pool := newMockStore(t)
	// SEC-5: GetProgress asserts the milestone is in the workspace (getInWorkspace) first.
	pool.ExpectQuery(`FROM milestones WHERE id = \$1 AND workspace_id = \$2`).
		WithArgs("m-1", "ws-1").
		WillReturnRows(milestoneRow("m-1", "v1.0", "active"))
	pool.ExpectQuery(`FROM issues WHERE milestone_id`).
		WithArgs("m-1").
		WillReturnRows(pgxmock.NewRows([]string{"total", "completed"}).AddRow(8, 3))

	p, err := store.GetProgress(context.Background(), "m-1", "ws-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	if p.TotalIssues != 8 || p.Completed != 3 {
		t.Errorf("counts wrong: %+v", p)
	}
	if p.CompletionPct < 0.37 || p.CompletionPct > 0.38 {
		t.Errorf("CompletionPct = %v, want ~0.375", p.CompletionPct)
	}
}
