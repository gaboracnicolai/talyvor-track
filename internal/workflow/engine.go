// Package workflow owns the per-team status pipeline.
//
// Default teams ship with the six built-in statuses (Backlog → Cancelled);
// any team can add custom ones. Each status belongs to one of five
// categories (backlog / unstarted / started / completed / cancelled) so
// downstream reports can group "any started status" without knowing the
// custom names a team picked.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type StatusCategory string

const (
	CategoryBacklog   StatusCategory = "backlog"
	CategoryUnstarted StatusCategory = "unstarted"
	CategoryStarted   StatusCategory = "started"
	CategoryCompleted StatusCategory = "completed"
	CategoryCancelled StatusCategory = "cancelled"
)

type WorkflowStatus struct {
	ID        string         `json:"id"         db:"id"`
	TeamID    string         `json:"team_id"    db:"team_id"`
	Name      string         `json:"name"       db:"name"`
	Color     string         `json:"color"      db:"color"`
	Category  StatusCategory `json:"category"   db:"category"`
	Position  int            `json:"position"   db:"position"`
	IsDefault bool           `json:"is_default" db:"is_default"`
}

type Engine struct {
	pool  pgxDB
	mu    sync.RWMutex
	cache map[string][]WorkflowStatus // teamID → ordered statuses
}

func New(pool *pgxpool.Pool) *Engine {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newEngine(db)
}

func newEngine(db pgxDB) *Engine {
	return &Engine{pool: db, cache: make(map[string][]WorkflowStatus)}
}

const statusColumns = `id, team_id, name, color, category, position, is_default`

func scanStatus(s interface{ Scan(...any) error }) (*WorkflowStatus, error) {
	var w WorkflowStatus
	var category string
	if err := s.Scan(&w.ID, &w.TeamID, &w.Name, &w.Color, &category, &w.Position, &w.IsDefault); err != nil {
		return nil, err
	}
	w.Category = StatusCategory(category)
	return &w, nil
}

// GetStatuses returns the team's statuses ordered by position. Reads
// hit the in-memory cache; misses populate it under the write lock.
func (e *Engine) GetStatuses(ctx context.Context, teamID string) ([]WorkflowStatus, error) {
	e.mu.RLock()
	cached, ok := e.cache[teamID]
	e.mu.RUnlock()
	if ok {
		// Return a copy so callers can't mutate engine state.
		out := make([]WorkflowStatus, len(cached))
		copy(out, cached)
		return out, nil
	}
	if e.pool == nil {
		return nil, nil
	}
	rows, err := e.pool.Query(ctx,
		`SELECT `+statusColumns+` FROM workflow_statuses WHERE team_id = $1 ORDER BY position ASC`,
		teamID,
	)
	if err != nil {
		return nil, fmt.Errorf("workflow: list: %w", err)
	}
	defer rows.Close()
	var out []WorkflowStatus
	for rows.Next() {
		st, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.cache[teamID] = append([]WorkflowStatus(nil), out...)
	e.mu.Unlock()
	return out, nil
}

// CreateStatus adds a status to a team. SEC-5 (tenancy): the write lands ONLY if the team
// is in the caller's SERVER-authorized workspace. workflow_statuses has no workspace_id
// column (scope via the teams parent), so the INSERT is gated by an EXISTS on
// teams(id, workspace_id) — a foreign or missing team inserts 0 rows → ErrNotFound
// (403 ≡ 404, no existence oracle), mirroring UpdateStatus/DeleteStatus.
func (e *Engine) CreateStatus(ctx context.Context, status WorkflowStatus, workspaceID string) (*WorkflowStatus, error) {
	if status.TeamID == "" || status.Name == "" {
		return nil, errors.New("workflow: TeamID and Name required")
	}
	if status.Color == "" {
		status.Color = "#94a3b8"
	}
	if status.Category == "" {
		status.Category = CategoryUnstarted
	}
	created, err := scanStatus(e.pool.QueryRow(ctx,
		`INSERT INTO workflow_statuses (team_id, name, color, category, position, is_default)
        SELECT $1, $2, $3, $4, $5, $6
        WHERE EXISTS (SELECT 1 FROM teams WHERE id = $1 AND workspace_id = $7)
        RETURNING `+statusColumns,
		status.TeamID, status.Name, status.Color, string(status.Category), status.Position, status.IsDefault, workspaceID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.invalidate(status.TeamID)
	return created, nil
}

// UpdateStatus changes the user-facing fields of a status — name,
// color, position. Category is locked after creation since reports
// across the workspace depend on the category mapping.
// ErrNotFound is the SEC-5 sentinel: a by-id status op resolved to no row in the caller's authorized
// workspace (statuses are scoped via their team). Handlers map it to 404 (no cross-tenant write, no oracle).
var ErrNotFound = errors.New("workflow: status not found in workspace")

func (e *Engine) UpdateStatus(ctx context.Context, id, workspaceID, name, color string, position int) (*WorkflowStatus, error) {
	// SEC-5: the status must belong to a team in workspaceID (the caller's authorized workspace).
	updated, err := scanStatus(e.pool.QueryRow(ctx,
		`UPDATE workflow_statuses SET name = $2, color = $3, position = $4
        WHERE id = $1 AND team_id IN (SELECT id FROM teams WHERE workspace_id = $5) RETURNING `+statusColumns,
		id, name, color, position, workspaceID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.invalidate(updated.TeamID)
	return updated, nil
}

// DeleteStatus refuses to drop a status that's still referenced by
// issues — orphaning issues would break the dashboard's group-by-status
// view. Callers should reassign the issues first.
func (e *Engine) DeleteStatus(ctx context.Context, id, workspaceID string) error {
	// Look up the status to find its team (for cache invalidation) AND its name (issues reference
	// statuses by name string today). SEC-5: scoped to a team in the caller's authorized workspace,
	// so a foreign status is ErrNotFound and nothing is deleted.
	var teamID, name string
	if err := e.pool.QueryRow(ctx,
		`SELECT team_id, name FROM workflow_statuses
        WHERE id = $1 AND team_id IN (SELECT id FROM teams WHERE workspace_id = $2)`, id, workspaceID,
	).Scan(&teamID, &name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	var active int
	if err := e.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issues WHERE team_id = $1 AND status = $2`, teamID, name,
	).Scan(&active); err != nil {
		return fmt.Errorf("workflow: count active issues: %w", err)
	}
	if active > 0 {
		return fmt.Errorf("workflow: cannot delete status with %d active issues", active)
	}

	if _, err := e.pool.Exec(ctx,
		`DELETE FROM workflow_statuses WHERE id = $1 AND team_id IN (SELECT id FROM teams WHERE workspace_id = $2)`,
		id, workspaceID); err != nil {
		return err
	}
	e.invalidate(teamID)
	return nil
}

// SeedDefaults registers the six default statuses for a brand-new
// team. Called once at team creation; safe to call repeatedly because
// the UNIQUE(team_id, name) constraint absorbs duplicates.
func (e *Engine) SeedDefaults(ctx context.Context, teamID string) error {
	if e.pool == nil {
		return nil
	}
	defaults := []WorkflowStatus{
		{Name: "Backlog", Color: "#94a3b8", Category: CategoryBacklog, Position: 0, IsDefault: true},
		{Name: "Todo", Color: "#94a3b8", Category: CategoryUnstarted, Position: 1, IsDefault: true},
		{Name: "In Progress", Color: "#3b82f6", Category: CategoryStarted, Position: 2, IsDefault: true},
		{Name: "In Review", Color: "#f59e0b", Category: CategoryStarted, Position: 3, IsDefault: true},
		{Name: "Done", Color: "#10b981", Category: CategoryCompleted, Position: 4, IsDefault: true},
		{Name: "Cancelled", Color: "#ef4444", Category: CategoryCancelled, Position: 5, IsDefault: true},
	}
	for _, s := range defaults {
		s.TeamID = teamID
		if _, err := e.pool.Exec(ctx,
			`INSERT INTO workflow_statuses (team_id, name, color, category, position, is_default)
            VALUES ($1, $2, $3, $4, $5, $6)
            ON CONFLICT (team_id, name) DO NOTHING`,
			s.TeamID, s.Name, s.Color, string(s.Category), s.Position, s.IsDefault,
		); err != nil {
			return fmt.Errorf("workflow: seed %s: %w", s.Name, err)
		}
	}
	e.invalidate(teamID)
	return nil
}

// ValidateTransition checks whether moving from one status category
// to another is sensible. The two illegal transitions are documented
// in the spec; any other move is permitted.
//
// The signature returns error so callers can use it as a gate, but
// the issue handler currently logs and proceeds — workflow correctness
// is advisory in Phase 2.
func (e *Engine) ValidateTransition(from, to WorkflowStatus) error {
	if from.Category == CategoryCompleted && to.Category == CategoryBacklog {
		return fmt.Errorf("workflow: cannot transition directly from completed to backlog (move through unstarted first)")
	}
	if from.Category == CategoryCancelled && to.Category == CategoryStarted {
		return fmt.Errorf("workflow: cannot reopen a cancelled issue directly into a started status")
	}
	return nil
}

func (e *Engine) invalidate(teamID string) {
	e.mu.Lock()
	delete(e.cache, teamID)
	e.mu.Unlock()
}
