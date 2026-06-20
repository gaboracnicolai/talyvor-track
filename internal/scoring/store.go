// Package scoring implements RICE and ICE prioritisation scoring.
//
//   - RICE = (Reach × Impact × Confidence/100) / Effort
//   - ICE  = Impact × Confidence × Ease
//
// The computed score is stored alongside its inputs so the
// prioritised-backlog query can ORDER BY a numeric column instead
// of recomputing per row. Round to 1 dp (RICE) / nearest int (ICE)
// at write time so a re-rendered list never disagrees with the
// stored value.
package scoring

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/tenancy"
)

// ─── public types ───────────────────────────────────────────

type ScoringMethod string

const (
	ScoringRICE ScoringMethod = "rice"
	ScoringICE  ScoringMethod = "ice"
)

type RICEScore struct {
	Reach      float64 `json:"reach"`
	Impact     float64 `json:"impact"`
	Confidence float64 `json:"confidence"`
	Effort     float64 `json:"effort"`
	Score      float64 `json:"score"`
}

type ICEScore struct {
	Impact     float64 `json:"impact"`
	Confidence float64 `json:"confidence"`
	Ease       float64 `json:"ease"`
	Score      float64 `json:"score"`
}

type IssueScore struct {
	ID          string        `json:"id"`
	IssueID     string        `json:"issue_id"`
	WorkspaceID string        `json:"workspace_id"`
	Method      ScoringMethod `json:"method"`
	RICE        *RICEScore    `json:"rice,omitempty"`
	ICE         *ICEScore     `json:"ice,omitempty"`
	Notes       string        `json:"notes"`
	ScoredBy    string        `json:"scored_by"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type ScoredIssue struct {
	model.Issue
	Score     float64       `json:"score"`
	Method    ScoringMethod `json:"scoring_method"`
	ScoreRank int           `json:"score_rank"`
}

type ScoreSummary struct {
	TotalScored  int     `json:"total_scored"`
	TotalIssues  int     `json:"total_issues"`
	CoverageRate float64 `json:"coverage_pct"`
	AvgRICE      float64 `json:"avg_rice_score"`
	AvgICE       float64 `json:"avg_ice_score"`
	TopIssueID   string  `json:"top_issue_id"`
}

// ─── store ──────────────────────────────────────────────────

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct{ pool pgxDB }

func NewStore(pool *pgxpool.Pool) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newStore(db)
}

func newStore(db pgxDB) *Store { return &Store{pool: db} }

const scoreColumns = `id, issue_id, workspace_id, method,
    rice_reach, rice_impact, rice_confidence, rice_effort, rice_score,
    ice_impact, ice_confidence, ice_ease, ice_score,
    notes, scored_by, created_at, updated_at`

func scanScore(s interface{ Scan(...any) error }) (*IssueScore, error) {
	var (
		out                                                 IssueScore
		method                                              string
		riceReach, riceImpact, riceConf, riceEff, riceScore *float64
		iceImpact, iceConf, iceEase, iceScore               *float64
	)
	if err := s.Scan(
		&out.ID, &out.IssueID, &out.WorkspaceID, &method,
		&riceReach, &riceImpact, &riceConf, &riceEff, &riceScore,
		&iceImpact, &iceConf, &iceEase, &iceScore,
		&out.Notes, &out.ScoredBy, &out.CreatedAt, &out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	out.Method = ScoringMethod(method)
	if riceScore != nil {
		out.RICE = &RICEScore{
			Reach:      derefFloat(riceReach),
			Impact:     derefFloat(riceImpact),
			Confidence: derefFloat(riceConf),
			Effort:     derefFloat(riceEff),
			Score:      *riceScore,
		}
	}
	if iceScore != nil {
		out.ICE = &ICEScore{
			Impact:     derefFloat(iceImpact),
			Confidence: derefFloat(iceConf),
			Ease:       derefFloat(iceEase),
			Score:      *iceScore,
		}
	}
	return &out, nil
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// ─── SetScore ──────────────────────────────────────────────

// SetScore validates the supplied inputs, computes the score, and
// upserts. The chosen method's payload must be present; the other
// can be nil. The "other" columns get cleared on update so a switch
// from RICE → ICE leaves no stale numbers behind in the row.
func (s *Store) SetScore(ctx context.Context, issueID, workspaceID, memberID string, method ScoringMethod, rice *RICEScore, ice *ICEScore, notes string) (*IssueScore, error) {
	if s.pool == nil {
		return nil, errors.New("scoring: store has no pool")
	}
	if issueID == "" || workspaceID == "" {
		return nil, errors.New("scoring: issue_id and workspace_id required")
	}

	var (
		// SQL args order matches the INSERT column list below.
		riceReach, riceImpact, riceConf, riceEff, riceScore any
		iceImpact, iceConf, iceEase, iceScore               any
	)

	switch method {
	case ScoringRICE:
		if rice == nil {
			return nil, errors.New("scoring: RICE method requires a rice payload")
		}
		if rice.Effort <= 0 {
			return nil, errors.New("scoring: RICE effort must be > 0")
		}
		if rice.Confidence < 0 || rice.Confidence > 100 {
			return nil, errors.New("scoring: RICE confidence must be 0-100")
		}
		if rice.Reach < 0 || rice.Impact < 0 {
			return nil, errors.New("scoring: RICE reach and impact must be ≥ 0")
		}
		// Round to 1 dp at write time so display + storage agree.
		score := math.Round(((rice.Reach*rice.Impact*(rice.Confidence/100))/rice.Effort)*10) / 10
		riceReach = rice.Reach
		riceImpact = rice.Impact
		riceConf = rice.Confidence
		riceEff = rice.Effort
		riceScore = score
		// Ensure the ICE columns are NULL on an explicit RICE update.
		iceImpact, iceConf, iceEase, iceScore = (*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil)

	case ScoringICE:
		if ice == nil {
			return nil, errors.New("scoring: ICE method requires an ice payload")
		}
		for _, v := range []float64{ice.Impact, ice.Confidence, ice.Ease} {
			if v < 1 || v > 10 {
				return nil, errors.New("scoring: ICE values must be 1-10")
			}
		}
		score := math.Round(ice.Impact * ice.Confidence * ice.Ease)
		iceImpact = ice.Impact
		iceConf = ice.Confidence
		iceEase = ice.Ease
		iceScore = score
		riceReach, riceImpact, riceConf, riceEff, riceScore =
			(*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil), (*float64)(nil)

	default:
		return nil, fmt.Errorf("scoring: unknown method %q", method)
	}

	if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "issues", issueID, workspaceID); err != nil {
		return nil, err
	}
	return scanScore(s.pool.QueryRow(ctx,
		`INSERT INTO issue_scores (issue_id, workspace_id, method,
            rice_reach, rice_impact, rice_confidence, rice_effort, rice_score,
            ice_impact, ice_confidence, ice_ease, ice_score,
            notes, scored_by)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
        ON CONFLICT (issue_id) DO UPDATE SET
            method = EXCLUDED.method,
            rice_reach = EXCLUDED.rice_reach,
            rice_impact = EXCLUDED.rice_impact,
            rice_confidence = EXCLUDED.rice_confidence,
            rice_effort = EXCLUDED.rice_effort,
            rice_score = EXCLUDED.rice_score,
            ice_impact = EXCLUDED.ice_impact,
            ice_confidence = EXCLUDED.ice_confidence,
            ice_ease = EXCLUDED.ice_ease,
            ice_score = EXCLUDED.ice_score,
            notes = EXCLUDED.notes,
            scored_by = EXCLUDED.scored_by,
            updated_at = NOW()
        RETURNING `+scoreColumns,
		issueID, workspaceID, string(method),
		riceReach, riceImpact, riceConf, riceEff, riceScore,
		iceImpact, iceConf, iceEase, iceScore,
		notes, memberID,
	))
}

// ─── GetScore ───────────────────────────────────────────────

func (s *Store) GetScore(ctx context.Context, issueID string) (*IssueScore, error) {
	if s.pool == nil {
		return nil, errors.New("scoring: store has no pool")
	}
	return scanScore(s.pool.QueryRow(ctx,
		`SELECT `+scoreColumns+` FROM issue_scores WHERE issue_id = $1`,
		issueID,
	))
}

// IssueScores reads the two denormalised score columns for a single
// issue. Returned as separate pointers so the issue.Store's
// scoreReader interface can stay tiny — nil means unscored, a
// non-nil zero means "scored as zero" (parked / no-op work).
func (s *Store) IssueScores(ctx context.Context, issueID string) (*float64, *float64, error) {
	if s.pool == nil {
		return nil, nil, nil
	}
	var rice, ice *float64
	err := s.pool.QueryRow(ctx,
		`SELECT rice_score, ice_score FROM issue_scores WHERE issue_id = $1`,
		issueID,
	).Scan(&rice, &ice)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("scoring: issue scores: %w", err)
	}
	return rice, ice, nil
}

// ─── DeleteScore ────────────────────────────────────────────

func (s *Store) DeleteScore(ctx context.Context, issueID string) error {
	if s.pool == nil {
		return errors.New("scoring: store has no pool")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM issue_scores WHERE issue_id = $1`, issueID)
	return err
}

// ─── GetPrioritizedBacklog ─────────────────────────────────

// GetPrioritizedBacklog returns issues + their scores ordered by the
// method-specific score column. Unscored issues land at the tail
// (NULLS LAST) and carry ScoreRank=0 so the UI can grey them out.
//
// One LEFT JOIN issues→issue_scores instead of two round trips —
// keeps the prioritisation view a single query for a workspace.
func (s *Store) GetPrioritizedBacklog(ctx context.Context, workspaceID string, teamID *string, method ScoringMethod, limit int) ([]ScoredIssue, error) {
	if s.pool == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	column := "rice_score"
	if method == ScoringICE {
		column = "ice_score"
	}

	args := []any{workspaceID}
	teamClause := ""
	if teamID != nil {
		teamClause = " AND i.team_id = $2"
		args = append(args, *teamID)
	}
	args = append(args, limit)
	limitArg := len(args)

	sql := `SELECT i.id, i.workspace_id, i.team_id, i.project_id, i.number, i.identifier,
                i.title, i.description, i.status, i.priority,
                i.assignee_id, i.creator_id, i.cycle_id, i.parent_id,
                i.due_date, i.completed_at,
                i.lens_feature, i.ai_cost_usd, i.ai_tokens,
                i.labels, i.sort_order, i.created_at, i.updated_at,
                s.` + column + ` AS score
            FROM issues i
            LEFT JOIN issue_scores s ON s.issue_id = i.id
            WHERE i.workspace_id = $1 AND i.status NOT IN ('cancelled')` +
		teamClause +
		fmt.Sprintf(" ORDER BY s.%s DESC NULLS LAST, i.priority ASC, i.created_at DESC LIMIT $%d", column, limitArg)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("scoring: prioritized: %w", err)
	}
	defer rows.Close()

	var out []ScoredIssue
	rank := 0
	for rows.Next() {
		var (
			si       ScoredIssue
			status   string
			priority int
			score    *float64
		)
		if err := rows.Scan(
			&si.ID, &si.WorkspaceID, &si.TeamID, &si.ProjectID, &si.Number, &si.Identifier,
			&si.Title, &si.Description, &status, &priority,
			&si.AssigneeID, &si.CreatorID, &si.CycleID, &si.ParentID,
			&si.DueDate, &si.CompletedAt,
			&si.LensFeature, &si.AICostUSD, &si.AITokens,
			&si.Labels, &si.SortOrder, &si.CreatedAt, &si.UpdatedAt,
			&score,
		); err != nil {
			return nil, err
		}
		si.Status = model.IssueStatus(status)
		si.Priority = model.IssuePriority(priority)
		si.Method = method
		if score != nil {
			rank++
			si.Score = *score
			si.ScoreRank = rank
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

// ─── GetScoreSummary ────────────────────────────────────────

// GetScoreSummary collapses the workspace's scoring state into one
// row — one query joining issues to scores. Coverage is computed
// in Go so we don't have to special-case division-by-zero in SQL.
func (s *Store) GetScoreSummary(ctx context.Context, workspaceID string) (*ScoreSummary, error) {
	if s.pool == nil {
		return &ScoreSummary{}, nil
	}
	var (
		totalIssues, totalScored int64
		avgRice, avgIce          float64
		topIssueID               string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT
            (SELECT COUNT(*) FROM issues WHERE workspace_id = $1 AND status != 'cancelled') AS total_issues,
            (SELECT COUNT(*) FROM issue_scores WHERE workspace_id = $1) AS total_scored,
            (SELECT COALESCE(AVG(rice_score), 0) FROM issue_scores WHERE workspace_id = $1 AND rice_score IS NOT NULL) AS avg_rice,
            (SELECT COALESCE(AVG(ice_score), 0) FROM issue_scores WHERE workspace_id = $1 AND ice_score IS NOT NULL) AS avg_ice,
            (SELECT COALESCE(issue_id, '') FROM issue_scores WHERE workspace_id = $1
                ORDER BY GREATEST(COALESCE(rice_score, 0), COALESCE(ice_score, 0)) DESC LIMIT 1) AS top_issue_id`,
		workspaceID,
	).Scan(&totalIssues, &totalScored, &avgRice, &avgIce, &topIssueID)
	if err != nil {
		return nil, fmt.Errorf("scoring: summary: %w", err)
	}
	out := &ScoreSummary{
		TotalIssues: int(totalIssues),
		TotalScored: int(totalScored),
		AvgRICE:     avgRice,
		AvgICE:      avgIce,
		TopIssueID:  strings.TrimSpace(topIssueID),
	}
	if totalIssues > 0 {
		out.CoverageRate = (float64(totalScored) / float64(totalIssues)) * 100.0
	}
	return out, nil
}
