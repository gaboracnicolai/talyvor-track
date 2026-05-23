// Package label owns the labels table. Labels exist at one of two
// scopes: workspace-wide (team_id IS NULL — visible to every team)
// or team-specific (team_id set — only that team's issues see them).
package label

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Label struct {
	ID          string    `json:"id"                db:"id"`
	WorkspaceID string    `json:"workspace_id"      db:"workspace_id"`
	TeamID      *string   `json:"team_id,omitempty" db:"team_id"`
	Name        string    `json:"name"              db:"name"`
	Color       string    `json:"color"             db:"color"`
	Description string    `json:"description"       db:"description"`
	CreatedAt   time.Time `json:"created_at"        db:"created_at"`
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

const labelColumns = `id, workspace_id, team_id, name, color, description, created_at`

func scanLabel(s interface{ Scan(...any) error }) (*Label, error) {
	var l Label
	if err := s.Scan(&l.ID, &l.WorkspaceID, &l.TeamID, &l.Name, &l.Color, &l.Description, &l.CreatedAt); err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *Store) Create(ctx context.Context, l Label) (*Label, error) {
	if l.WorkspaceID == "" || l.Name == "" {
		return nil, errors.New("label: WorkspaceID and Name required")
	}
	if l.Color == "" {
		l.Color = "#94a3b8"
	}
	return scanLabel(s.pool.QueryRow(ctx,
		`INSERT INTO labels (workspace_id, team_id, name, color, description)
        VALUES ($1, $2, $3, $4, $5) RETURNING `+labelColumns,
		l.WorkspaceID, l.TeamID, l.Name, l.Color, l.Description,
	))
}

// List returns every label visible to the caller. When teamID is set,
// the result is workspace-wide labels + that team's labels. When
// teamID is empty, only workspace-wide labels are returned.
func (s *Store) List(ctx context.Context, workspaceID, teamID string) ([]Label, error) {
	if s.pool == nil {
		return nil, nil
	}
	var (
		rows pgx.Rows
		err  error
	)
	if teamID == "" {
		rows, err = s.pool.Query(ctx,
			`SELECT `+labelColumns+` FROM labels
            WHERE workspace_id = $1 AND team_id IS NULL
            ORDER BY name ASC`,
			workspaceID,
		)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT `+labelColumns+` FROM labels
            WHERE workspace_id = $1 AND (team_id IS NULL OR team_id = $2)
            ORDER BY team_id NULLS FIRST, name ASC`,
			workspaceID, teamID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("label: list: %w", err)
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM labels WHERE id = $1`, id)
	return err
}
