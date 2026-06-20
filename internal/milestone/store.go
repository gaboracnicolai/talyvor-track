// Package milestone owns the milestones table — named checkpoints
// inside a project. Each milestone groups a subset of the project's
// issues. Progress is computed live from the issues table.
package milestone

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/tenancy"
)

type Milestone struct {
	ID          string     `json:"id"                     db:"id"`
	WorkspaceID string     `json:"workspace_id"           db:"workspace_id"`
	ProjectID   string     `json:"project_id"             db:"project_id"`
	Name        string     `json:"name"                   db:"name"`
	Description string     `json:"description"            db:"description"`
	Status      string     `json:"status"                 db:"status"`
	TargetDate  *time.Time `json:"target_date,omitempty"  db:"target_date"`
	CompletedAt *time.Time `json:"completed_at,omitempty" db:"completed_at"`
	CreatedAt   time.Time  `json:"created_at"             db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"             db:"updated_at"`
}

type Progress struct {
	MilestoneID   string  `json:"milestone_id"`
	TotalIssues   int     `json:"total_issues"`
	Completed     int     `json:"completed"`
	CompletionPct float64 `json:"completion_pct"`
}

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

const milestoneColumns = `id, workspace_id, project_id, name, description, status,
    target_date, completed_at, created_at, updated_at`

func scanMilestone(s interface{ Scan(...any) error }) (*Milestone, error) {
	var m Milestone
	if err := s.Scan(
		&m.ID, &m.WorkspaceID, &m.ProjectID, &m.Name, &m.Description, &m.Status,
		&m.TargetDate, &m.CompletedAt, &m.CreatedAt, &m.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) Create(ctx context.Context, m Milestone) (*Milestone, error) {
	if m.WorkspaceID == "" || m.ProjectID == "" || m.Name == "" {
		return nil, errors.New("milestone: WorkspaceID, ProjectID, and Name required")
	}
	if m.Status == "" {
		m.Status = "upcoming"
	}
	if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "projects", m.ProjectID, m.WorkspaceID); err != nil {
		return nil, err
	}
	return scanMilestone(s.pool.QueryRow(ctx,
		`INSERT INTO milestones (workspace_id, project_id, name, description, status, target_date)
        VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+milestoneColumns,
		m.WorkspaceID, m.ProjectID, m.Name, m.Description, m.Status, m.TargetDate,
	))
}

func (s *Store) GetByID(ctx context.Context, id string) (*Milestone, error) {
	return scanMilestone(s.pool.QueryRow(ctx,
		`SELECT `+milestoneColumns+` FROM milestones WHERE id = $1`, id,
	))
}

func (s *Store) ListByProject(ctx context.Context, projectID string) ([]Milestone, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+milestoneColumns+` FROM milestones WHERE project_id = $1
        ORDER BY target_date NULLS LAST, created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("milestone: list: %w", err)
	}
	defer rows.Close()
	var out []Milestone
	for rows.Next() {
		m, err := scanMilestone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

var updatable = map[string]struct{}{
	"name": {}, "description": {}, "status": {}, "target_date": {}, "completed_at": {},
}

func (s *Store) Update(ctx context.Context, id string, updates map[string]any) (*Milestone, error) {
	if len(updates) == 0 {
		return s.GetByID(ctx, id)
	}
	var (
		setClauses []string
		args       []any
		argN       int
	)
	for k, v := range updates {
		if _, ok := updatable[k]; !ok {
			continue
		}
		argN++
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", k, argN))
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return s.GetByID(ctx, id)
	}
	argN++
	args = append(args, id)
	sql := fmt.Sprintf(
		`UPDATE milestones SET %s, updated_at = NOW() WHERE id = $%d RETURNING %s`,
		joinComma(setClauses), argN, milestoneColumns,
	)
	return scanMilestone(s.pool.QueryRow(ctx, sql, args...))
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// GetProgress counts issues attached to this milestone and the
// fraction that have reached the done status. One query.
func (s *Store) GetProgress(ctx context.Context, id string) (*Progress, error) {
	p := &Progress{MilestoneID: id}
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE status = 'done')
        FROM issues WHERE milestone_id = $1`,
		id,
	).Scan(&p.TotalIssues, &p.Completed); err != nil {
		return nil, fmt.Errorf("milestone: progress: %w", err)
	}
	if p.TotalIssues > 0 {
		p.CompletionPct = float64(p.Completed) / float64(p.TotalIssues)
	}
	return p, nil
}
