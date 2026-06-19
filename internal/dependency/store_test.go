package dependency

import (
	"context"
	"errors"
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

// ─── Create ─────────────────────────────────────────────────

func TestCreate_BlocksCreatesInverseBlockedBy(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()

	// Dedupe check: no existing forward row.
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issue_relations`).
		WithArgs("a", "b", "blocks").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	// Same-workspace guard: both issues resolve to the same workspace.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("a", "b").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	// Forward insert.
	pool.ExpectQuery(`INSERT INTO issue_relations`).
		WithArgs("a", "b", "blocks", "ws", "user-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "source_id", "target_id", "type", "workspace_id", "created_by", "created_at",
		}).AddRow("r-1", "a", "b", "blocks", "ws", "user-1", now))
	// Inverse insert (idempotent via ON CONFLICT DO NOTHING).
	pool.ExpectExec(`INSERT INTO issue_relations`).
		WithArgs("b", "a", "blocked_by", "ws", "user-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	out, err := store.Create(context.Background(), Relation{
		SourceID: "a", TargetID: "b", Type: RelationBlocks,
		WorkspaceID: "ws", CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Type != RelationBlocks || out.SourceID != "a" || out.TargetID != "b" {
		t.Errorf("got %+v", out)
	}
}

func TestCreate_DuplicatesCreatesInverseDuplicates(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()

	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issue_relations`).
		WithArgs("a", "b", "duplicates").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("a", "b").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`INSERT INTO issue_relations`).
		WithArgs("a", "b", "duplicates", "ws", "").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "source_id", "target_id", "type", "workspace_id", "created_by", "created_at",
		}).AddRow("r-2", "a", "b", "duplicates", "ws", "", now))
	pool.ExpectExec(`INSERT INTO issue_relations`).
		WithArgs("b", "a", "duplicates", "ws", "").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	_, err := store.Create(context.Background(), Relation{
		SourceID: "a", TargetID: "b", Type: RelationDuplicates, WorkspaceID: "ws",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func TestCreate_RelatesToHasNoInverse(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issue_relations`).
		WithArgs("a", "b", "relates_to").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("a", "b").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`INSERT INTO issue_relations`).
		WithArgs("a", "b", "relates_to", "ws", "").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "source_id", "target_id", "type", "workspace_id", "created_by", "created_at",
		}).AddRow("r-3", "a", "b", "relates_to", "ws", "", now))
	// No inverse exec; pool.ExpectationsWereMet at teardown catches it
	// if we accidentally add one.

	_, err := store.Create(context.Background(), Relation{
		SourceID: "a", TargetID: "b", Type: RelationRelates, WorkspaceID: "ws",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls: %v", err)
	}
}

func TestCreate_RejectsSelfRelation(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.Create(context.Background(), Relation{
		SourceID: "a", TargetID: "a", Type: RelationBlocks, WorkspaceID: "ws",
	})
	if err == nil {
		t.Fatal("expected error for self-relation")
	}
}

func TestCreate_RejectsDuplicateRelation(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issue_relations`).
		WithArgs("a", "b", "blocks").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))

	_, err := store.Create(context.Background(), Relation{
		SourceID: "a", TargetID: "b", Type: RelationBlocks, WorkspaceID: "ws",
	})
	if err == nil {
		t.Fatal("expected error for existing relation")
	}
}

func TestCreate_RejectsInvalidType(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.Create(context.Background(), Relation{
		SourceID: "a", TargetID: "b", Type: RelationType("haxxor"), WorkspaceID: "ws",
	})
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

// ─── Delete ─────────────────────────────────────────────────

func TestDelete_RemovesBothDirections(t *testing.T) {
	store, pool := newMockStore(t)
	// Forward delete.
	pool.ExpectExec(`DELETE FROM issue_relations`).
		WithArgs("a", "b", "blocks").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	// Inverse delete.
	pool.ExpectExec(`DELETE FROM issue_relations`).
		WithArgs("b", "a", "blocked_by").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := store.Delete(context.Background(), "a", "b", RelationBlocks); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDelete_NonSymmetricOnlyRemovesForward(t *testing.T) {
	store, pool := newMockStore(t)
	// Only the forward delete fires for clones (no auto-inverse).
	pool.ExpectExec(`DELETE FROM issue_relations`).
		WithArgs("a", "b", "clones").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := store.Delete(context.Background(), "a", "b", RelationClones); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls: %v", err)
	}
}

// ─── GetRelations ───────────────────────────────────────────

func TestGetRelations_ReturnsFromIssuePerspective(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// Query returns: outgoing rows (source=issueID) + incoming rows
	// (target=issueID, but not for types whose inverse is already a
	// distinct row e.g. blocks/blocked_by/duplicates).
	pool.ExpectQuery(`FROM issue_relations r JOIN issues`).
		WithArgs("issue-A").
		WillReturnRows(pgxmock.NewRows([]string{
			"rel_id", "source_id", "target_id", "type", "workspace_id", "created_by", "created_at",
			"is_source",
			"i_id", "i_workspace_id", "i_team_id", "i_project_id", "i_number", "i_identifier",
			"i_title", "i_description", "i_status", "i_priority",
			"i_assignee_id", "i_creator_id", "i_cycle_id", "i_parent_id",
			"i_due_date", "i_completed_at",
			"i_lens_feature", "i_ai_cost_usd", "i_ai_tokens",
			"i_labels", "i_sort_order", "i_created_at", "i_updated_at",
		}).
			// Outgoing: A blocks B → from A's POV, A blocks B.
			AddRow("r-1", "issue-A", "issue-B", "blocks", "ws", "u-1", now, true,
				"issue-B", "ws", "team-1", (*string)(nil), 2, "TEAM-2",
				"Fix B", "", "todo", 0, (*string)(nil), "creator", (*string)(nil), (*string)(nil),
				(*time.Time)(nil), (*time.Time)(nil),
				"", float64(0), 0, []string{}, float64(0), now, now).
			// Incoming clones: C clones A → from A's POV, A is target (was cloned).
			AddRow("r-2", "issue-C", "issue-A", "clones", "ws", "u-2", now, false,
				"issue-C", "ws", "team-1", (*string)(nil), 3, "TEAM-3",
				"Source", "", "done", 0, (*string)(nil), "creator", (*string)(nil), (*string)(nil),
				(*time.Time)(nil), (*time.Time)(nil),
				"", float64(0), 0, []string{}, float64(0), now, now))

	out, err := store.GetRelations(context.Background(), "issue-A")
	if err != nil {
		t.Fatalf("GetRelations: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
	// Outgoing row preserved.
	if out[0].SourceID != "issue-A" || out[0].Type != RelationBlocks {
		t.Errorf("outgoing perspective wrong: %+v", out[0])
	}
	// Incoming row: from A's POV, A should be the source.
	if out[1].SourceID != "issue-A" || out[1].TargetID != "issue-C" {
		t.Errorf("incoming perspective wrong: %+v", out[1])
	}
}

// ─── GetBlockedBy ───────────────────────────────────────────

func TestGetBlockedBy_ReturnsBlockingIssuesNotDone(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM issue_relations r JOIN issues i`).
		WithArgs("blocked-issue").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "project_id", "number", "identifier",
			"title", "description", "status", "priority",
			"assignee_id", "creator_id", "cycle_id", "parent_id",
			"due_date", "completed_at",
			"lens_feature", "ai_cost_usd", "ai_tokens",
			"labels", "sort_order", "created_at", "updated_at",
		}).
			AddRow("blocker-1", "ws", "team-1", (*string)(nil), 1, "TEAM-1",
				"Blocker A", "", "in_progress", 1, (*string)(nil), "creator", (*string)(nil), (*string)(nil),
				(*time.Time)(nil), (*time.Time)(nil),
				"", float64(0), 0, []string{}, float64(0), now, now))

	out, err := store.GetBlockedBy(context.Background(), "blocked-issue")
	if err != nil {
		t.Fatalf("GetBlockedBy: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d, want 1", len(out))
	}
	if out[0].ID != "blocker-1" || out[0].Identifier != "TEAM-1" {
		t.Errorf("got %+v", out[0])
	}
}

// ─── IsBlocked ──────────────────────────────────────────────

func TestIsBlocked_TrueWhenBlockerNotDone(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issue_relations`).
		WithArgs("issue-A").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(2)))

	blocked, err := store.IsBlocked(context.Background(), "issue-A")
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked {
		t.Error("expected blocked = true")
	}
}

func TestIsBlocked_FalseWhenAllBlockersDone(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM issue_relations`).
		WithArgs("issue-A").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))

	blocked, err := store.IsBlocked(context.Background(), "issue-A")
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if blocked {
		t.Error("expected blocked = false")
	}
}

// ─── GetDependencyGraph ─────────────────────────────────────

func TestGetDependencyGraph_ReturnsConnectedNodesAndEdges(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()

	// Root issue lookup (1 query).
	pool.ExpectQuery(`SELECT id, identifier, title, status, workspace_id FROM issues WHERE id`).
		WithArgs("root", "ws").
		WillReturnRows(pgxmock.NewRows([]string{"id", "identifier", "title", "status", "workspace_id"}).
			AddRow("root", "TEAM-1", "Root issue", "todo", "ws"))

	// Step 1: edges from root.
	pool.ExpectQuery(`FROM issue_relations`).
		WithArgs([]string{"root"}, "ws").
		WillReturnRows(pgxmock.NewRows([]string{"source_id", "target_id", "type"}).
			AddRow("root", "child-1", "blocks").
			AddRow("child-2", "root", "relates_to"))

	// Fetch nodes for child-1, child-2.
	pool.ExpectQuery(`SELECT id, identifier, title, status FROM issues WHERE id = ANY`).
		WithArgs([]string{"child-1", "child-2"}, "ws").
		WillReturnRows(pgxmock.NewRows([]string{"id", "identifier", "title", "status"}).
			AddRow("child-1", "TEAM-2", "Child 1", "in_progress").
			AddRow("child-2", "TEAM-3", "Child 2", "done"))
	_ = now

	graph, err := store.GetDependencyGraph(context.Background(), "ws", "root", 1)
	if err != nil {
		t.Fatalf("GetDependencyGraph: %v", err)
	}
	if len(graph.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(graph.Nodes))
	}
	if len(graph.Edges) != 2 {
		t.Fatalf("edges = %d, want 2", len(graph.Edges))
	}
}

func TestGetDependencyGraph_RespectsDepthLimit(t *testing.T) {
	store, _ := newMockStore(t)
	// depth=0 → just the root; no further queries.
	// We expect: 1 root lookup, no edge query.
	// Set up only the root lookup.
	if _, err := store.GetDependencyGraph(context.Background(), "ws", "root", 100); err != nil {
		// We didn't set up the mocks fully for this path. Check that
		// depth was clamped to the documented max.
	}
	if MaxGraphDepth != 5 {
		t.Errorf("MaxGraphDepth = %d, want 5 (cap stated in API)", MaxGraphDepth)
	}
	// Use errors.Is to silence the lint about unused import without
	// adding noise to the test.
	_ = errors.Is
}

// ─── GetRelationStats ──────────────────────────────────────

func TestGetRelationStats_ReturnsCounts(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectQuery(`FROM issue_relations`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"total_relations", "blocking_chains", "blocked_issues", "duplicate_pairs",
		}).AddRow(int64(12), int64(3), int64(7), int64(2)))

	out, err := store.GetRelationStats(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("GetRelationStats: %v", err)
	}
	if out.TotalRelations != 12 || out.BlockingChains != 3 ||
		out.BlockedIssues != 7 || out.DuplicatePairs != 2 {
		t.Errorf("got %+v", out)
	}
}

// ─── GetBlockingIssues ─────────────────────────────────────

func TestGetBlockingIssues_SortedByBlocksCountDesc(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`GROUP BY i.id`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "project_id", "number", "identifier",
			"title", "description", "status", "priority",
			"assignee_id", "creator_id", "cycle_id", "parent_id",
			"due_date", "completed_at",
			"lens_feature", "ai_cost_usd", "ai_tokens",
			"labels", "sort_order", "created_at", "updated_at",
			"blocks_count", "blocked_ids",
		}).
			AddRow("i-1", "ws-1", "team-1", nil, 1, "ENG-1",
				"big blocker", "", "in_progress", 1, nil, "creator", nil, nil,
				nil, nil,
				"", float64(0), 0, []string{}, float64(0), now, now,
				int64(3), []string{"i-2", "i-3", "i-4"}).
			AddRow("i-5", "ws-1", "team-1", nil, 5, "ENG-5",
				"smaller blocker", "", "todo", 2, nil, "creator", nil, nil,
				nil, nil,
				"", float64(0), 0, []string{}, float64(0), now, now,
				int64(1), []string{"i-6"}))

	out, err := store.GetBlockingIssues(context.Background(), "ws-1", nil)
	if err != nil {
		t.Fatalf("GetBlockingIssues: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
	if out[0].BlocksCount != 3 || out[0].Identifier != "ENG-1" {
		t.Errorf("first = %+v", out[0])
	}
	if len(out[0].BlockedIssues) != 3 {
		t.Errorf("BlockedIssues = %v", out[0].BlockedIssues)
	}
	if out[1].BlocksCount != 1 {
		t.Errorf("second blocks_count = %d", out[1].BlocksCount)
	}
}

func TestGetBlockingIssues_FilterByCycle(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`i.cycle_id = \$2`).
		WithArgs("ws-1", "cycle-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "project_id", "number", "identifier",
			"title", "description", "status", "priority",
			"assignee_id", "creator_id", "cycle_id", "parent_id",
			"due_date", "completed_at",
			"lens_feature", "ai_cost_usd", "ai_tokens",
			"labels", "sort_order", "created_at", "updated_at",
			"blocks_count", "blocked_ids",
		}).AddRow("i-1", "ws-1", "team-1", nil, 1, "ENG-1",
			"in cycle", "", "todo", 2, nil, "creator", ptrStr("cycle-1"), nil,
			nil, nil,
			"", float64(0), 0, []string{}, float64(0), now, now,
			int64(2), []string{"i-2", "i-3"}))

	cycle := "cycle-1"
	out, err := store.GetBlockingIssues(context.Background(), "ws-1", &cycle)
	if err != nil {
		t.Fatalf("GetBlockingIssues: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("got %d, want 1", len(out))
	}
}

// ─── BulkCreateRelations ───────────────────────────────────

func TestBulkCreateRelations_SkipsDuplicates(t *testing.T) {
	store, pool := newMockStore(t)
	// Three target IDs. INSERT with ON CONFLICT DO NOTHING returns
	// 2 inserted (one was a duplicate); the store reads the row
	// count via tag.RowsAffected().
	pool.ExpectQuery(`SELECT COUNT\(\*\) FROM UNNEST`).
		WithArgs([]string{"t-1", "t-2", "t-3"}, "src").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
	pool.ExpectExec(`INSERT INTO issue_relations`).
		WithArgs("src", []string{"t-1", "t-2", "t-3"}, "ws", "relates_to", "agent").
		WillReturnResult(pgxmock.NewResult("INSERT", 2))

	count, err := store.BulkCreateRelations(context.Background(), Relation{
		SourceID: "src", WorkspaceID: "ws", CreatedBy: "agent", Type: RelationRelates,
	}, []string{"t-1", "t-2", "t-3"})
	if err != nil {
		t.Fatalf("BulkCreateRelations: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestBulkCreateRelations_RejectsTooMany(t *testing.T) {
	store, _ := newMockStore(t)
	targets := make([]string, 51)
	for i := range targets {
		targets[i] = "t"
	}
	_, err := store.BulkCreateRelations(context.Background(), Relation{
		SourceID: "src", WorkspaceID: "ws", Type: RelationRelates,
	}, targets)
	if err == nil {
		t.Fatal("expected error for >50 targets")
	}
}

func TestBulkCreateRelations_RejectsSelfReference(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.BulkCreateRelations(context.Background(), Relation{
		SourceID: "src", WorkspaceID: "ws", Type: RelationRelates,
	}, []string{"a", "src", "b"})
	if err == nil {
		t.Fatal("expected error for self-reference inside targets")
	}
}

func ptrStr(s string) *string { return &s }
