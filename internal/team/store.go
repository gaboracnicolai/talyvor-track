// Package team owns the teams table. Each team has a short
// identifier ("ENG", "MKT") that becomes the prefix in every issue
// number — so identifier choice is permanent and must be validated
// before insert.
package team

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/model"
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

const teamColumns = `id, workspace_id, name, identifier, color, icon, created_at, updated_at`

func scanTeam(s interface{ Scan(...any) error }) (*model.Team, error) {
	var t model.Team
	if err := s.Scan(&t.ID, &t.WorkspaceID, &t.Name, &t.Identifier, &t.Color, &t.Icon, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) Create(ctx context.Context, t model.Team) (*model.Team, error) {
	if t.WorkspaceID == "" || t.Name == "" || t.Identifier == "" {
		return nil, errors.New("team: WorkspaceID, Name, and Identifier are required")
	}
	if t.Color == "" {
		t.Color = "#6366f1"
	}
	return scanTeam(s.pool.QueryRow(ctx,
		`INSERT INTO teams (workspace_id, name, identifier, color, icon)
        VALUES ($1, $2, $3, $4, $5) RETURNING `+teamColumns,
		t.WorkspaceID, t.Name, t.Identifier, t.Color, t.Icon,
	))
}

func (s *Store) GetByID(ctx context.Context, id string) (*model.Team, error) {
	return scanTeam(s.pool.QueryRow(ctx,
		`SELECT `+teamColumns+` FROM teams WHERE id = $1`, id))
}

func (s *Store) ListByWorkspace(ctx context.Context, workspaceID string) ([]model.Team, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+teamColumns+` FROM teams WHERE workspace_id = $1 ORDER BY name ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("team: list: %w", err)
	}
	defer rows.Close()
	var out []model.Team
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

var teamUpdatable = map[string]struct{}{
	"name": {}, "identifier": {}, "color": {}, "icon": {},
}

func (s *Store) Update(ctx context.Context, id string, updates map[string]any) (*model.Team, error) {
	if len(updates) == 0 {
		return s.GetByID(ctx, id)
	}
	var (
		setClauses []string
		args       []any
		argN       int
	)
	for k, v := range updates {
		if _, ok := teamUpdatable[k]; !ok {
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
		`UPDATE teams SET %s, updated_at = NOW() WHERE id = $%d RETURNING %s`,
		joinComma(setClauses), argN, teamColumns,
	)
	return scanTeam(s.pool.QueryRow(ctx, sql, args...))
}

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM teams WHERE id = $1`, id)
	return err
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
