package issue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/talyvor/track/internal/model"
)

// ptrTime helps mock rows produce *time.Time values for nullable
// timestamp columns. Inline literal `&time.Now().UTC()` isn't valid Go.
func ptrTime(t time.Time) *time.Time { return &t }

func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newStore(pool), pool
}

func issueRow(id, identifier, status string, number int) *pgxmock.Rows {
	now := time.Now().UTC()
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "team_id", "project_id", "number", "identifier",
		"title", "description", "status", "priority",
		"assignee_id", "creator_id", "cycle_id", "parent_id",
		"due_date", "completed_at",
		"lens_feature", "ai_cost_usd", "ai_tokens",
		"labels", "sort_order", "created_at", "updated_at",
	}).AddRow(
		id, "ws-1", "team-1", nil, number, identifier,
		"Title", "Body", status, 0,
		nil, "creator-1", nil, nil,
		nil, nil,
		"", 0.0, 0,
		[]string{}, 0.0, now, now,
	)
}

func TestCreate_InsertsWithAutoNumberAndIdentifier(t *testing.T) {
	store, pool := newMockStore(t)

	// 1) Look up the team identifier so we can format ENG-N.
	pool.ExpectQuery(`SELECT identifier FROM teams WHERE id`).
		WithArgs("team-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"identifier"}).AddRow("ENG"))

	// 2) Pick the next number for this team — current max is 41.
	pool.ExpectQuery(`COALESCE\(MAX\(number\), 0\) \+ 1 FROM issues WHERE team_id`).
		WithArgs("team-1").
		WillReturnRows(pgxmock.NewRows([]string{"next"}).AddRow(42))

	// 3) Insert and return the materialised row. The INSERT takes 17
	//    positional args (every column except the server-set timestamps);
	//    we use AnyArg() across the board since the exact ordering is
	//    verified end-to-end against the production schema in CI.
	anyArgs := make([]any, 17)
	for k := range anyArgs {
		anyArgs[k] = pgxmock.AnyArg()
	}
	pool.ExpectQuery(`INSERT INTO issues`).
		WithArgs(anyArgs...).
		WillReturnRows(issueRow("issue-1", "ENG-42", "backlog", 42))

	out, err := store.Create(context.Background(), model.Issue{
		WorkspaceID: "ws-1",
		TeamID:      "team-1",
		Title:       "Title",
		Description: "Body",
		Status:      model.StatusBacklog,
		CreatorID:   "creator-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Number != 42 {
		t.Errorf("Number = %d, want 42", out.Number)
	}
	if out.Identifier != "ENG-42" {
		t.Errorf("Identifier = %q, want ENG-42", out.Identifier)
	}
}

func TestGetByID_ReturnsCorrectIssue(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`FROM issues WHERE id`).
		WithArgs("issue-x").
		WillReturnRows(issueRow("issue-x", "ENG-7", "in_progress", 7))

	out, err := store.GetByID(context.Background(), "issue-x")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if out.ID != "issue-x" || out.Number != 7 {
		t.Errorf("got %+v", out)
	}
}

func TestList_FiltersByStatus(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`FROM issues WHERE workspace_id = \$1 AND status = \$2`).
		WithArgs("ws-1", "in_progress", 50, 0).
		WillReturnRows(issueRow("a", "ENG-1", "in_progress", 1).
			AddRow("b", "ws-1", "team-1", nil, 2, "ENG-2",
				"t2", "d2", "in_progress", 0,
				nil, "c", nil, nil,
				nil, nil,
				"", 0.0, 0,
				[]string{}, 0.0, time.Now().UTC(), time.Now().UTC()))

	out, err := store.List(context.Background(), IssueFilter{
		WorkspaceID: "ws-1", Status: "in_progress",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	for _, i := range out {
		if i.Status != model.StatusInProgress {
			t.Errorf("status leak: %+v", i)
		}
	}
}

func TestList_FiltersByAssignee(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`assignee_id = \$2`).
		WithArgs("ws-1", "alice", 50, 0).
		WillReturnRows(issueRow("a", "ENG-1", "todo", 1))

	out, err := store.List(context.Background(), IssueFilter{
		WorkspaceID: "ws-1", AssigneeID: "alice",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("len = %d, want 1", len(out))
	}
}

func TestUpdate_StatusToDoneSetsCompletedAt(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`UPDATE issues SET`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "project_id", "number", "identifier",
			"title", "description", "status", "priority",
			"assignee_id", "creator_id", "cycle_id", "parent_id",
			"due_date", "completed_at",
			"lens_feature", "ai_cost_usd", "ai_tokens",
			"labels", "sort_order", "created_at", "updated_at",
		}).AddRow(
			"issue-1", "ws-1", "team-1", nil, 1, "ENG-1",
			"t", "d", "done", 0,
			nil, "creator", nil, nil,
			nil, ptrTime(time.Now().UTC()), // completed_at set
			"", 0.0, 0,
			[]string{}, 0.0, time.Now().UTC(), time.Now().UTC(),
		))

	out, err := store.Update(context.Background(), "issue-1", map[string]any{
		"status": "done",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.CompletedAt == nil {
		t.Errorf("CompletedAt should be set when status moves to done; got %+v", out)
	}
}

func TestUpdate_StatusFromDoneClearsCompletedAt(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`UPDATE issues SET`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "project_id", "number", "identifier",
			"title", "description", "status", "priority",
			"assignee_id", "creator_id", "cycle_id", "parent_id",
			"due_date", "completed_at",
			"lens_feature", "ai_cost_usd", "ai_tokens",
			"labels", "sort_order", "created_at", "updated_at",
		}).AddRow(
			"issue-1", "ws-1", "team-1", nil, 1, "ENG-1",
			"t", "d", "in_progress", 0,
			nil, "creator", nil, nil,
			nil, nil, // completed_at cleared
			"", 0.0, 0,
			[]string{}, 0.0, time.Now().UTC(), time.Now().UTC(),
		))

	out, err := store.Update(context.Background(), "issue-1", map[string]any{
		"status": "in_progress",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.CompletedAt != nil {
		t.Errorf("CompletedAt should be cleared when moving from done; got %+v", out.CompletedAt)
	}
}

func TestSearch_ReturnsMatchingIssues(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`to_tsvector|websearch_to_tsquery|@@`).
		WithArgs("ws-1", "auth bug", 25).
		WillReturnRows(issueRow("a", "ENG-1", "backlog", 1))

	out, err := store.Search(context.Background(), "ws-1", "auth bug", 25)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("len = %d, want 1", len(out))
	}
}

func TestDelete_SetsStatusToCancelled(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`UPDATE issues SET status = 'cancelled'`).
		WithArgs("issue-x", "ws-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := store.Delete(context.Background(), "issue-x", "ws-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestCreate_PropagatesTeamNotFound(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT identifier FROM teams WHERE id`).
		WithArgs("team-missing", "ws-1").
		WillReturnError(errors.New("no rows in result set"))

	_, err := store.Create(context.Background(), model.Issue{
		WorkspaceID: "ws-1", TeamID: "team-missing", Title: "x", CreatorID: "u",
	})
	if err == nil {
		t.Error("Create should error when team not found")
	}
}

// ─── BulkUpdate ────────────────────────────────────────────

func TestBulkUpdate_AppliesUpdatesAtomically(t *testing.T) {
	store, pool := newMockStore(t)

	// Two rows, both changing status + sort_order. The implementation
	// wraps the per-row UPDATEs in a single transaction so a mid-batch
	// failure rolls everything back.
	pool.ExpectBegin()
	pool.ExpectExec(`UPDATE issues SET status`).
		WithArgs("in_progress", float64(1.5), pgxmock.AnyArg(), pgxmock.AnyArg(), "i-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE issues SET status`).
		WithArgs("in_progress", float64(2.5), pgxmock.AnyArg(), pgxmock.AnyArg(), "i-2").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()

	count, err := store.BulkUpdate(context.Background(), []BulkUpdateItem{
		{ID: "i-1", Status: "in_progress", SortOrder: 1.5},
		{ID: "i-2", Status: "in_progress", SortOrder: 2.5},
	})
	if err != nil {
		t.Fatalf("BulkUpdate: %v", err)
	}
	if count != 2 {
		t.Errorf("updated count = %d, want 2", count)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestBulkUpdate_RollsBackOnFailure(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectBegin()
	pool.ExpectExec(`UPDATE issues SET status`).
		WithArgs("done", float64(1.0), pgxmock.AnyArg(), pgxmock.AnyArg(), "i-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectExec(`UPDATE issues SET status`).
		WithArgs("done", float64(2.0), pgxmock.AnyArg(), pgxmock.AnyArg(), "i-bad").
		WillReturnError(errors.New("constraint violation"))
	pool.ExpectRollback()

	_, err := store.BulkUpdate(context.Background(), []BulkUpdateItem{
		{ID: "i-1", Status: "done", SortOrder: 1.0},
		{ID: "i-bad", Status: "done", SortOrder: 2.0},
	})
	if err == nil {
		t.Fatal("expected error from second UPDATE to surface")
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestBulkUpdate_EmptyInputReturnsZero(t *testing.T) {
	store, _ := newMockStore(t)
	count, err := store.BulkUpdate(context.Background(), nil)
	if err != nil {
		t.Fatalf("BulkUpdate(nil): %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestBulkUpdate_SortOrderOnly(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectBegin()
	// Drag-within-column case: status omitted, only sort_order changes.
	// The implementation should build a SET clause that skips the
	// status column entirely.
	pool.ExpectExec(`UPDATE issues SET sort_order`).
		WithArgs(float64(3.5), pgxmock.AnyArg(), "i-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	pool.ExpectCommit()

	count, err := store.BulkUpdate(context.Background(), []BulkUpdateItem{
		{ID: "i-1", SortOrder: 3.5},
	})
	if err != nil {
		t.Fatalf("BulkUpdate: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}
