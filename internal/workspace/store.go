// Package workspace owns the workspaces table — the top-level tenant
// boundary for every other resource. Slug uniqueness is enforced at
// the database level; the store layer just surfaces the violation
// cleanly when it happens.
package workspace

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

const workspaceColumns = `id, name, slug, logo_url, plan, created_at, updated_at`

func scanWorkspace(s interface{ Scan(...any) error }) (*model.Workspace, error) {
	var w model.Workspace
	if err := s.Scan(&w.ID, &w.Name, &w.Slug, &w.LogoURL, &w.Plan, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *Store) Create(ctx context.Context, ws model.Workspace) (*model.Workspace, error) {
	if ws.Name == "" || ws.Slug == "" {
		return nil, errors.New("workspace: Name and Slug required")
	}
	if ws.Plan == "" {
		ws.Plan = "free"
	}
	return scanWorkspace(s.pool.QueryRow(ctx,
		`INSERT INTO workspaces (name, slug, logo_url, plan)
        VALUES ($1, $2, $3, $4) RETURNING `+workspaceColumns,
		ws.Name, ws.Slug, ws.LogoURL, ws.Plan,
	))
}

func (s *Store) GetByID(ctx context.Context, id string) (*model.Workspace, error) {
	return scanWorkspace(s.pool.QueryRow(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE id = $1`, id,
	))
}

func (s *Store) GetBySlug(ctx context.Context, slug string) (*model.Workspace, error) {
	return scanWorkspace(s.pool.QueryRow(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE slug = $1`, slug,
	))
}

func (s *Store) List(ctx context.Context) ([]model.Workspace, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("workspace: list: %w", err)
	}
	defer rows.Close()
	var out []model.Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

var workspaceUpdatable = map[string]struct{}{
	"name": {}, "slug": {}, "logo_url": {}, "plan": {},
}

func (s *Store) Update(ctx context.Context, id string, updates map[string]any) (*model.Workspace, error) {
	if len(updates) == 0 {
		return s.GetByID(ctx, id)
	}
	var (
		setClauses []string
		args       []any
		argN       int
	)
	for k, v := range updates {
		if _, ok := workspaceUpdatable[k]; !ok {
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
		`UPDATE workspaces SET %s, updated_at = NOW() WHERE id = $%d RETURNING %s`,
		joinComma(setClauses), argN, workspaceColumns,
	)
	return scanWorkspace(s.pool.QueryRow(ctx, sql, args...))
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

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, id)
	return err
}

// ListIDs returns just the workspace IDs. Used by the Lens syncer to
// iterate every workspace on its tick without paying for the full
// row scan.
func (s *Store) ListIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspaces`)
	if err != nil {
		return nil, fmt.Errorf("workspace: list ids: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
