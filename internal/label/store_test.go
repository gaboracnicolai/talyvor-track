package label

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

func TestCreate_WorkspaceWideLabel(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`INSERT INTO labels`).
		WithArgs("ws-1", pgxmock.AnyArg(), "Bug", "#ef4444", "").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "color", "description", "created_at",
		}).AddRow("l-1", "ws-1", nil, "Bug", "#ef4444", "", time.Now().UTC()))

	out, err := store.Create(context.Background(), Label{
		WorkspaceID: "ws-1", Name: "Bug", Color: "#ef4444",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.TeamID != nil {
		t.Errorf("TeamID should be nil for workspace-wide label; got %v", out.TeamID)
	}
}

func TestCreate_TeamSpecificLabel(t *testing.T) {
	teamID := "team-1"
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`INSERT INTO labels`).
		WithArgs("ws-1", &teamID, "Frontend", "#3b82f6", "").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "color", "description", "created_at",
		}).AddRow("l-2", "ws-1", &teamID, "Frontend", "#3b82f6", "", time.Now().UTC()))

	out, err := store.Create(context.Background(), Label{
		WorkspaceID: "ws-1", TeamID: &teamID, Name: "Frontend", Color: "#3b82f6",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.TeamID == nil || *out.TeamID != "team-1" {
		t.Errorf("TeamID should be set to team-1; got %v", out.TeamID)
	}
}

func TestList_ReturnsBothWorkspaceAndTeamLabels(t *testing.T) {
	teamID := "team-1"
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM labels`).
		WithArgs("ws-1", "team-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "name", "color", "description", "created_at",
		}).
			AddRow("l-1", "ws-1", nil, "Bug", "#ef4444", "", now).
			AddRow("l-2", "ws-1", &teamID, "Frontend", "#3b82f6", "", now))

	out, err := store.List(context.Background(), "ws-1", "team-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
	var foundWs, foundTeam bool
	for _, l := range out {
		if l.TeamID == nil {
			foundWs = true
		} else {
			foundTeam = true
		}
	}
	if !foundWs || !foundTeam {
		t.Errorf("expected both workspace and team labels; got %+v", out)
	}
}

func TestDelete_RemovesLabel(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`DELETE FROM labels`).
		WithArgs("l-1", "ws-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := store.Delete(context.Background(), "l-1", "ws-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
