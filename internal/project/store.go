// Package project owns the projects table. Projects group issues
// across a time-bounded effort and are nested under a team.
package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/tenancy"
)

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

const projectColumns = `id, workspace_id, team_id, name, identifier, description, status,
    priority, start_date, target_date, created_at, updated_at`

func scanProject(s interface{ Scan(...any) error }) (*model.Project, error) {
	var p model.Project
	if err := s.Scan(
		&p.ID, &p.WorkspaceID, &p.TeamID, &p.Name, &p.Identifier, &p.Description, &p.Status,
		&p.Priority, &p.StartDate, &p.TargetDate, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) Create(ctx context.Context, p model.Project) (*model.Project, error) {
	if p.WorkspaceID == "" || p.TeamID == "" || p.Name == "" || p.Identifier == "" {
		return nil, errors.New("project: WorkspaceID, TeamID, Name, and Identifier required")
	}
	if p.Status == "" {
		p.Status = "active"
	}
	if p.TeamID != "" {
		if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "teams", p.TeamID, p.WorkspaceID); err != nil {
			return nil, err
		}
	}
	return scanProject(s.pool.QueryRow(ctx,
		`INSERT INTO projects (workspace_id, team_id, name, identifier, description, status,
            priority, start_date, target_date)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+projectColumns,
		p.WorkspaceID, p.TeamID, p.Name, p.Identifier, p.Description, p.Status,
		p.Priority, p.StartDate, p.TargetDate,
	))
}

func (s *Store) GetByID(ctx context.Context, id string) (*model.Project, error) {
	return scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
}

func (s *Store) ListByWorkspace(ctx context.Context, workspaceID string) ([]model.Project, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE workspace_id = $1 ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("project: list: %w", err)
	}
	defer rows.Close()
	var out []model.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

var projectUpdatable = map[string]struct{}{
	"name": {}, "identifier": {}, "description": {}, "status": {},
	"priority": {}, "start_date": {}, "target_date": {},
}

// ErrNotFound is the SEC-5 sentinel: a by-id operation resolved to no row IN THE CALLER'S
// AUTHORIZED WORKSPACE. Handlers map it to 404 so a foreign id and a nonexistent id are
// indistinguishable (no existence oracle).
var ErrNotFound = errors.New("project: not found in workspace")

// Update mutates a project only within workspaceID (the caller's authorized workspace) —
// SEC-5: a foreign id yields ErrNotFound, never a cross-tenant write. workspaceID comes from
// the authz context, never the URL/body.
func (s *Store) Update(ctx context.Context, id, workspaceID string, updates map[string]any) (*model.Project, error) {
	var (
		setClauses []string
		args       []any
		argN       int
	)
	for k, v := range updates {
		if _, ok := projectUpdatable[k]; !ok {
			continue
		}
		argN++
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", k, argN))
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return s.getInWorkspace(ctx, id, workspaceID) // scoped no-op fetch (never unscoped GetByID)
	}
	argN++
	idN := argN
	args = append(args, id)
	argN++
	wsN := argN
	args = append(args, workspaceID)
	sql := fmt.Sprintf(
		`UPDATE projects SET %s, updated_at = NOW() WHERE id = $%d AND workspace_id = $%d RETURNING %s`,
		joinComma(setClauses), idN, wsN, projectColumns,
	)
	p, err := scanProject(s.pool.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// getInWorkspace is the scoped read the by-id ops fall back to (never the unscoped GetByID).
func (s *Store) getInWorkspace(ctx context.Context, id, workspaceID string) (*model.Project, error) {
	p, err := scanProject(s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1 AND workspace_id = $2`, id, workspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Store) Delete(ctx context.Context, id, workspaceID string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
