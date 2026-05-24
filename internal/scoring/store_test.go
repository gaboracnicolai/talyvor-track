package scoring

import (
	"context"
	"math"
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

func ptr[T any](v T) *T { return &v }

func scoreRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "issue_id", "workspace_id", "method",
		"rice_reach", "rice_impact", "rice_confidence", "rice_effort", "rice_score",
		"ice_impact", "ice_confidence", "ice_ease", "ice_score",
		"notes", "scored_by", "created_at", "updated_at",
	})
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.05
}

// ─── SetScore RICE ──────────────────────────────────────────

func TestSetScore_RICECalculatesCorrectly(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// RICE = (R * I * C/100) / E = (1000 * 2 * 80/100) / 4 = 400
	// The store rounds to 1 decimal place: 400.0
	pool.ExpectQuery(`INSERT INTO issue_scores`).
		WithArgs("i-1", "ws-1", "rice",
			float64(1000), float64(2), float64(80), float64(4), float64(400),
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			"strong evidence", "member-1").
		WillReturnRows(scoreRows().AddRow(
			"s-1", "i-1", "ws-1", "rice",
			ptr(float64(1000)), ptr(float64(2)), ptr(float64(80)), ptr(float64(4)), ptr(float64(400)),
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			"strong evidence", "member-1", now, now,
		))

	out, err := store.SetScore(context.Background(), "i-1", "ws-1", "member-1", ScoringRICE,
		&RICEScore{Reach: 1000, Impact: 2, Confidence: 80, Effort: 4},
		nil, "strong evidence")
	if err != nil {
		t.Fatalf("SetScore: %v", err)
	}
	if out.RICE == nil || !approxEqual(out.RICE.Score, 400.0) {
		t.Errorf("RICE score = %+v, want ~400", out.RICE)
	}
}

func TestSetScore_RICERoundsToOneDecimal(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// (100 * 1 * 75/100) / 7 = 75/7 = 10.714... → 10.7
	pool.ExpectQuery(`INSERT INTO issue_scores`).
		WithArgs("i-1", "ws-1", "rice",
			float64(100), float64(1), float64(75), float64(7), float64(10.7),
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			"", "").
		WillReturnRows(scoreRows().AddRow(
			"s-1", "i-1", "ws-1", "rice",
			ptr(float64(100)), ptr(float64(1)), ptr(float64(75)), ptr(float64(7)), ptr(float64(10.7)),
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			"", "", now, now,
		))

	out, err := store.SetScore(context.Background(), "i-1", "ws-1", "", ScoringRICE,
		&RICEScore{Reach: 100, Impact: 1, Confidence: 75, Effort: 7}, nil, "")
	if err != nil {
		t.Fatalf("SetScore: %v", err)
	}
	if !approxEqual(out.RICE.Score, 10.7) {
		t.Errorf("RICE score = %v, want 10.7", out.RICE.Score)
	}
}

func TestSetScore_RICERejectsZeroEffort(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.SetScore(context.Background(), "i-1", "ws-1", "", ScoringRICE,
		&RICEScore{Reach: 100, Impact: 1, Confidence: 50, Effort: 0}, nil, "")
	if err == nil {
		t.Fatal("expected error for zero effort")
	}
}

func TestSetScore_RICERejectsBadConfidence(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.SetScore(context.Background(), "i-1", "ws-1", "", ScoringRICE,
		&RICEScore{Reach: 100, Impact: 1, Confidence: 150, Effort: 2}, nil, "")
	if err == nil {
		t.Fatal("expected error for confidence > 100")
	}
}

// ─── SetScore ICE ───────────────────────────────────────────

func TestSetScore_ICECalculatesCorrectly(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// ICE = I*C*E = 8*7*6 = 336 (integer)
	pool.ExpectQuery(`INSERT INTO issue_scores`).
		WithArgs("i-1", "ws-1", "ice",
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			float64(8), float64(7), float64(6), float64(336),
			"", "").
		WillReturnRows(scoreRows().AddRow(
			"s-1", "i-1", "ws-1", "ice",
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			ptr(float64(8)), ptr(float64(7)), ptr(float64(6)), ptr(float64(336)),
			"", "", now, now,
		))

	out, err := store.SetScore(context.Background(), "i-1", "ws-1", "", ScoringICE,
		nil, &ICEScore{Impact: 8, Confidence: 7, Ease: 6}, "")
	if err != nil {
		t.Fatalf("SetScore: %v", err)
	}
	if out.ICE == nil || out.ICE.Score != 336 {
		t.Errorf("ICE score = %+v, want 336", out.ICE)
	}
}

func TestSetScore_ICERoundsToInteger(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// 5.5 * 6.7 * 8.1 = 298.485 → 298 (nearest int)
	pool.ExpectQuery(`INSERT INTO issue_scores`).
		WithArgs("i-1", "ws-1", "ice",
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			float64(5.5), float64(6.7), float64(8.1), float64(298),
			"", "").
		WillReturnRows(scoreRows().AddRow(
			"s-1", "i-1", "ws-1", "ice",
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			ptr(float64(5.5)), ptr(float64(6.7)), ptr(float64(8.1)), ptr(float64(298)),
			"", "", now, now,
		))

	out, err := store.SetScore(context.Background(), "i-1", "ws-1", "", ScoringICE,
		nil, &ICEScore{Impact: 5.5, Confidence: 6.7, Ease: 8.1}, "")
	if err != nil {
		t.Fatalf("SetScore: %v", err)
	}
	if out.ICE.Score != 298 {
		t.Errorf("ICE score = %v, want 298", out.ICE.Score)
	}
}

func TestSetScore_ICERejectsOutOfRange(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.SetScore(context.Background(), "i-1", "ws-1", "", ScoringICE,
		nil, &ICEScore{Impact: 11, Confidence: 5, Ease: 5}, "")
	if err == nil {
		t.Fatal("expected error for impact > 10")
	}
	_, err = store.SetScore(context.Background(), "i-1", "ws-1", "", ScoringICE,
		nil, &ICEScore{Impact: 5, Confidence: 0.5, Ease: 5}, "")
	if err == nil {
		t.Fatal("expected error for confidence < 1")
	}
}

func TestSetScore_RejectsMissingPayloadForMethod(t *testing.T) {
	store, _ := newMockStore(t)
	if _, err := store.SetScore(context.Background(), "i-1", "ws-1", "", ScoringRICE,
		nil, nil, ""); err == nil {
		t.Error("expected error: rice method needs rice payload")
	}
}

// ─── GetScore ───────────────────────────────────────────────

func TestGetScore_ReturnsStoredScore(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT .* FROM issue_scores WHERE issue_id`).
		WithArgs("i-1").
		WillReturnRows(scoreRows().AddRow(
			"s-1", "i-1", "ws-1", "rice",
			ptr(float64(500)), ptr(float64(1)), ptr(float64(60)), ptr(float64(2)), ptr(float64(150)),
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil),
			"go go go", "member-1", now, now,
		))

	out, err := store.GetScore(context.Background(), "i-1")
	if err != nil {
		t.Fatalf("GetScore: %v", err)
	}
	if out.RICE == nil || out.RICE.Score != 150 {
		t.Errorf("RICE = %+v", out.RICE)
	}
	if out.Notes != "go go go" {
		t.Errorf("notes = %q", out.Notes)
	}
}

// ─── DeleteScore ────────────────────────────────────────────

func TestDeleteScore_RemovesRow(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`DELETE FROM issue_scores WHERE issue_id`).
		WithArgs("i-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	if err := store.DeleteScore(context.Background(), "i-1"); err != nil {
		t.Fatalf("DeleteScore: %v", err)
	}
}

// ─── GetPrioritizedBacklog ──────────────────────────────────

func TestGetPrioritizedBacklog_OrdersByScoreDescNullsLast(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()

	pool.ExpectQuery(`LEFT JOIN issue_scores`).
		WithArgs("ws-1", 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "project_id", "number", "identifier",
			"title", "description", "status", "priority",
			"assignee_id", "creator_id", "cycle_id", "parent_id",
			"due_date", "completed_at",
			"lens_feature", "ai_cost_usd", "ai_tokens",
			"labels", "sort_order", "created_at", "updated_at",
			"score",
		}).
			AddRow("i-1", "ws-1", "team-1", nil, 1, "ENG-1",
				"high", "", "todo", 0, nil, "u", nil, nil, nil, nil,
				"", float64(0), 0, []string{}, float64(0), now, now,
				ptr(float64(400))).
			AddRow("i-2", "ws-1", "team-1", nil, 2, "ENG-2",
				"low", "", "todo", 0, nil, "u", nil, nil, nil, nil,
				"", float64(0), 0, []string{}, float64(0), now, now,
				ptr(float64(50))).
			AddRow("i-3", "ws-1", "team-1", nil, 3, "ENG-3",
				"unscored", "", "todo", 0, nil, "u", nil, nil, nil, nil,
				"", float64(0), 0, []string{}, float64(0), now, now,
				(*float64)(nil)))

	out, err := store.GetPrioritizedBacklog(context.Background(), "ws-1", nil, ScoringRICE, 50)
	if err != nil {
		t.Fatalf("GetPrioritizedBacklog: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d, want 3", len(out))
	}
	if out[0].Identifier != "ENG-1" || out[0].ScoreRank != 1 {
		t.Errorf("first = %+v", out[0])
	}
	if out[1].ScoreRank != 2 {
		t.Errorf("second rank = %d", out[1].ScoreRank)
	}
	// Unscored issues are rank 0 (or last) — the ranking helper
	// shouldn't count them as ranked.
	if out[2].ScoreRank != 0 {
		t.Errorf("unscored should have rank 0, got %d", out[2].ScoreRank)
	}
	if out[2].Score != 0 {
		t.Errorf("unscored should have score 0, got %v", out[2].Score)
	}
}

func TestGetPrioritizedBacklog_FiltersByTeam(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`i.team_id = \$2`).
		WithArgs("ws-1", "team-1", 50).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "workspace_id", "team_id", "project_id", "number", "identifier",
			"title", "description", "status", "priority",
			"assignee_id", "creator_id", "cycle_id", "parent_id",
			"due_date", "completed_at",
			"lens_feature", "ai_cost_usd", "ai_tokens",
			"labels", "sort_order", "created_at", "updated_at",
			"score",
		}).AddRow("i-1", "ws-1", "team-1", nil, 1, "ENG-1",
			"hi", "", "todo", 0, nil, "u", nil, nil, nil, nil,
			"", float64(0), 0, []string{}, float64(0), now, now,
			ptr(float64(99))))

	team := "team-1"
	out, err := store.GetPrioritizedBacklog(context.Background(), "ws-1", &team, ScoringRICE, 50)
	if err != nil {
		t.Fatalf("GetPrioritizedBacklog: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("got %d, want 1", len(out))
	}
}

// ─── GetScoreSummary ────────────────────────────────────────

func TestGetScoreSummary_CalculatesCoverage(t *testing.T) {
	store, pool := newMockStore(t)
	// 60 total non-cancelled, 30 scored → 50% coverage.
	pool.ExpectQuery(`SELECT.*COUNT.*FROM issues.*FROM issue_scores`).
		WithArgs("ws-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"total_issues", "total_scored", "avg_rice", "avg_ice", "top_issue_id",
		}).AddRow(int64(60), int64(30), float64(120.5), float64(280), "i-top"))

	out, err := store.GetScoreSummary(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("GetScoreSummary: %v", err)
	}
	if out.TotalIssues != 60 || out.TotalScored != 30 {
		t.Errorf("counts = %+v", out)
	}
	if out.CoverageRate < 49.9 || out.CoverageRate > 50.1 {
		t.Errorf("coverage = %v, want 50", out.CoverageRate)
	}
	if out.TopIssueID != "i-top" {
		t.Errorf("top = %q", out.TopIssueID)
	}
}
