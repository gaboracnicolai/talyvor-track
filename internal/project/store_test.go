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
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
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
		WithArgs("completed", "p-1", "ws-1").
		WillReturnRows(projRow("p-1", "PRJ-1", "completed"))

	out, err := store.Update(context.Background(), "p-1", "ws-1", map[string]any{"status": "completed"})
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
		WithArgs("p-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := store.Delete(context.Background(), "p-1", "ws-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// ─── GetRoadmap ────────────────────────────────────────────

func TestGetRoadmap_ReturnsProjectsInDateRange(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	start := now
	end := now.AddDate(0, 6, 0)

	// Project rollup query (one row per project).
	pool.ExpectQuery(`FROM projects p`).
		WithArgs("ws-1", start, end).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "identifier", "description", "status",
			"priority", "start_date", "target_date", "created_at", "updated_at",
			"team_name", "issue_count", "completed_count", "ai_cost_usd",
		}).
			AddRow("p-1", "ws-1", "team-1", "Roadmap A", "PRJ-A", "", "active",
				0, &now, ptrTime(now.AddDate(0, 2, 0)), now, now,
				"Engineering", int64(10), int64(4), float64(12.34)).
			AddRow("p-2", "ws-1", "team-1", "Roadmap B", "PRJ-B", "", "active",
				0, &now, ptrTime(now.AddDate(0, 1, 0)), now, now,
				"Engineering", int64(5), int64(5), float64(2.5)))

	// Per-project milestone rollup. The implementation issues one
	// follow-up query, scoped to the project IDs collected above.
	pool.ExpectQuery(`FROM milestones m`).
		WithArgs([]string{"p-1", "p-2"}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "project_id", "name", "description", "status",
			"target_date", "completed_at", "created_at", "updated_at",
			"issue_count", "completed_count", "ai_cost_usd",
		}).
			AddRow("m-1", "ws-1", "p-1", "Beta", "", "upcoming",
				ptrTime(now.AddDate(0, 1, 0)), (*time.Time)(nil), now, now,
				int64(3), int64(1), float64(4.5)))

	out, err := store.GetRoadmap(context.Background(), "ws-1", nil, start, end)
	if err != nil {
		t.Fatalf("GetRoadmap: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d projects, want 2", len(out))
	}
	if out[0].TeamName != "Engineering" {
		t.Errorf("team_name = %q", out[0].TeamName)
	}
	if out[0].IssueCount != 10 || out[0].CompletedCount != 4 {
		t.Errorf("counts = %+v", out[0])
	}
	// 4/10 → 40% completion.
	if out[0].CompletionPct < 39.9 || out[0].CompletionPct > 40.1 {
		t.Errorf("CompletionPct = %v, want ~40", out[0].CompletionPct)
	}
	if out[0].AICostUSD != 12.34 {
		t.Errorf("AICostUSD = %v", out[0].AICostUSD)
	}
	if len(out[0].Milestones) != 1 {
		t.Errorf("milestones = %d, want 1", len(out[0].Milestones))
	}
	if out[0].Milestones[0].IssueCount != 3 || out[0].Milestones[0].AICostUSD != 4.5 {
		t.Errorf("milestone rollup wrong: %+v", out[0].Milestones[0])
	}
	// p-2 has 5/5 → 100% completion, no milestones.
	if out[1].CompletionPct < 99.9 {
		t.Errorf("p-2 CompletionPct = %v, want 100", out[1].CompletionPct)
	}
	if len(out[1].Milestones) != 0 {
		t.Errorf("p-2 should have no milestones, got %d", len(out[1].Milestones))
	}
}

func TestGetRoadmap_FiltersByTeamID(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	start := now
	end := now.AddDate(0, 6, 0)
	team := "team-1"

	// With team filter, the SQL adds AND p.team_id = $4. Args become
	// (workspaceID, start, end, teamID).
	pool.ExpectQuery(`p.team_id = \$4`).
		WithArgs("ws-1", start, end, team).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "identifier", "description", "status",
			"priority", "start_date", "target_date", "created_at", "updated_at",
			"team_name", "issue_count", "completed_count", "ai_cost_usd",
		}).
			AddRow("p-1", "ws-1", team, "Roadmap A", "PRJ-A", "", "active",
				0, &now, ptrTime(now.AddDate(0, 2, 0)), now, now,
				"Engineering", int64(0), int64(0), float64(0)))

	// The milestone follow-up query fires once project IDs have been
	// gathered, even when the team has no milestones — return an
	// empty rowset so it can complete.
	pool.ExpectQuery(`FROM milestones m`).
		WithArgs([]string{"p-1"}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "project_id", "name", "description", "status",
			"target_date", "completed_at", "created_at", "updated_at",
			"issue_count", "completed_count", "ai_cost_usd",
		}))

	out, err := store.GetRoadmap(context.Background(), "ws-1", &team, start, end)
	if err != nil {
		t.Fatalf("GetRoadmap: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d, want 1", len(out))
	}
	if out[0].TeamID != team {
		t.Errorf("team_id = %v", out[0].TeamID)
	}
}

func TestGetRoadmap_EmptyResults(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM projects p`).
		WithArgs("ws-1", now, now.AddDate(0, 6, 0)).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "identifier", "description", "status",
			"priority", "start_date", "target_date", "created_at", "updated_at",
			"team_name", "issue_count", "completed_count", "ai_cost_usd",
		}))

	out, err := store.GetRoadmap(context.Background(), "ws-1", nil, now, now.AddDate(0, 6, 0))
	if err != nil {
		t.Fatalf("GetRoadmap: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("got %d, want 0", len(out))
	}
}
