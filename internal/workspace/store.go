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
	Begin(ctx context.Context) (pgx.Tx, error)
}

// templateSeeder is the subset of template.Store that workspace
// calls on Create. The interface lives here so workspace doesn't
// import the template package directly — keeps the package graph
// one-way and the workspace tests free of template fixtures.
type templateSeeder interface {
	SeedDefaults(ctx context.Context, workspaceID string) error
}

type Store struct {
	pool   pgxDB
	seeder templateSeeder
}

func NewStore(pool *pgxpool.Pool) *Store {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newStore(db)
}

func newStore(db pgxDB) *Store { return &Store{pool: db} }

// WithTemplateSeeder wires the template store so workspace creation
// auto-seeds the canonical Bug / Feature / Tech-Debt / Incident /
// Task templates. Optional — without it, workspaces start empty.
func (s *Store) WithTemplateSeeder(t templateSeeder) *Store {
	s.seeder = t
	return s
}

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
	out, err := scanWorkspace(s.pool.QueryRow(ctx,
		`INSERT INTO workspaces (name, slug, logo_url, plan)
        VALUES ($1, $2, $3, $4) RETURNING `+workspaceColumns,
		ws.Name, ws.Slug, ws.LogoURL, ws.Plan,
	))
	if err != nil {
		return nil, err
	}
	// Best-effort template seed. A failure here never blocks workspace
	// creation — at worst the user opens an empty Templates page and
	// can re-trigger the seed manually.
	if s.seeder != nil {
		_ = s.seeder.SeedDefaults(ctx, out.ID)
	}
	return out, nil
}

// CreateWithOwner creates a workspace AND seeds its creator as the OWNER member in the
// SAME TRANSACTION (T10). The atomicity matters: a workspace with no members is
// unreachable (T10's own membership check would 403 everyone, including its creator), so
// a partial failure must leave neither. Role is 'owner' explicitly — the creator is the
// root of this workspace's permission structure. The member name defaults to the email
// (the gateway provides no name claim; display-only, editable later).
func (s *Store) CreateWithOwner(ctx context.Context, ws model.Workspace, ownerEmail string) (*model.Workspace, error) {
	if ws.Name == "" || ws.Slug == "" {
		return nil, errors.New("workspace: Name and Slug required")
	}
	if ownerEmail == "" {
		return nil, errors.New("workspace: owner email required")
	}
	if ws.Plan == "" {
		ws.Plan = "free"
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := scanWorkspace(tx.QueryRow(ctx,
		`INSERT INTO workspaces (name, slug, logo_url, plan) VALUES ($1, $2, $3, $4) RETURNING `+workspaceColumns,
		ws.Name, ws.Slug, ws.LogoURL, ws.Plan,
	))
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1, $2, $3, 'owner')`,
		out.ID, ownerEmail, ownerEmail,
	); err != nil {
		return nil, fmt.Errorf("workspace: seed owner member: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Best-effort, POST-commit: a template-seed failure must not roll back the workspace
	// + its owner.
	if s.seeder != nil {
		_ = s.seeder.SeedDefaults(ctx, out.ID)
	}
	return out, nil
}

// ListByIDs returns the workspaces with the given ids (T10: the caller's own workspaces,
// resolved from membership — so a list can never enumerate workspaces the caller is not a
// member of).
func (s *Store) ListByIDs(ctx context.Context, ids []string) ([]model.Workspace, error) {
	if len(ids) == 0 {
		return []model.Workspace{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+workspaceColumns+` FROM workspaces WHERE id = ANY($1) ORDER BY created_at DESC`,
		ids,
	)
	if err != nil {
		return nil, fmt.Errorf("workspace: list by ids: %w", err)
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
	//nosemgrep: operate-by-id-write-requires-workspace-scope-sprintf -- self-scoping: id IS the authorized workspace (tenant root, from authz.WorkspaceID); there is no separate workspace_id column to add (mirrors this store's Delete nosemgrep). INVALIDATED IF the handler ever passes a non-authz id here (anything other than authz.WorkspaceID), OR the workspaces table gains a separate scoping column (then this UPDATE must add AND <that_column> = $n).
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
	// nosemgrep: operate-by-id-write-requires-workspace-scope -- self-scoping: id IS the caller's authorized workspace (handler passes authz.WorkspaceID). The workspace is the tenant root; there is no parent workspace to scope to. INVALIDATED IF the handler ever passes a non-authz id here (anything other than authz.WorkspaceID), OR the workspaces table gains a separate scoping column (then this DELETE must add AND <that_column> = $n).
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
