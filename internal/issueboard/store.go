// Package issueboard is B27.30's public issue board: a workspace owner turns on a link, and anyone
// holding it can read the workspace's issues as a board — identifier, title, status and priority,
// nothing else — without signing in. Turning the link off ends it at once.
package issueboard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/tenancy"
)

// Share is one board link, as its workspace's members see it.
type Share struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	ProjectID   *string   `json:"project_id,omitempty"`
	Token       string    `json:"token"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// BoardIssue is the whole of what a public board says about an issue.
type BoardIssue struct {
	Identifier string    `json:"identifier"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Priority   int       `json:"priority"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Board is the anonymous answer for one link.
type Board struct {
	Workspace string       `json:"workspace"`
	Project   string       `json:"project,omitempty"`
	Issues    []BoardIssue `json:"issues"`
	// Truncated is true when the board holds more than MaxBoardIssues; the most recently updated are shown.
	Truncated bool `json:"truncated"`
}

// MaxBoardIssues bounds one anonymous read.
const MaxBoardIssues = 500

// ErrNotFound covers an unknown token, a revoked link and a deleted workspace alike, so the public
// route cannot be used to tell them apart.
var ErrNotFound = errors.New("issueboard: not found")

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
	return &Store{pool: db}
}

const shareColumns = `id, workspace_id, project_id, token, created_by, created_at`

func scanShare(s interface{ Scan(...any) error }) (*Share, error) {
	var sh Share
	if err := s.Scan(&sh.ID, &sh.WorkspaceID, &sh.ProjectID, &sh.Token, &sh.CreatedBy, &sh.CreatedAt); err != nil {
		return nil, err
	}
	return &sh, nil
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create turns on a link for the workspace, or for one of its projects when projectID is set.
func (s *Store) Create(ctx context.Context, workspaceID string, projectID *string, createdBy string) (*Share, error) {
	if workspaceID == "" || createdBy == "" {
		return nil, errors.New("issueboard: WorkspaceID and CreatedBy are required")
	}
	if projectID != nil && *projectID == "" {
		projectID = nil
	}
	if projectID != nil {
		if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "projects", *projectID, workspaceID); err != nil {
			return nil, err
		}
	}
	token, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("issueboard: token: %w", err)
	}
	return scanShare(s.pool.QueryRow(ctx,
		`INSERT INTO issue_board_shares (workspace_id, project_id, token, created_by)
         VALUES ($1, $2, $3, $4) RETURNING `+shareColumns,
		workspaceID, projectID, token, createdBy,
	))
}

// List answers the workspace's live links, newest first.
func (s *Store) List(ctx context.Context, workspaceID string) ([]Share, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+shareColumns+` FROM issue_board_shares
         WHERE workspace_id = $1 AND revoked_at IS NULL
         ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("issueboard: list: %w", err)
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sh)
	}
	return out, rows.Err()
}

// Revoke turns a link off. A link already off, or another workspace's, is ErrNotFound.
func (s *Store) Revoke(ctx context.Context, id, workspaceID string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE issue_board_shares SET revoked_at = NOW()
         WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL`,
		id, workspaceID,
	)
	if err != nil {
		return fmt.Errorf("issueboard: revoke: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Public answers the board behind a live link.
func (s *Store) Public(ctx context.Context, token string) (*Board, error) {
	var (
		workspaceID string
		projectID   *string
		b           Board
		project     *string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT sh.workspace_id, sh.project_id, w.name, p.name
         FROM issue_board_shares sh
         JOIN workspaces w ON w.id = sh.workspace_id AND w.deleted_at IS NULL
         LEFT JOIN projects p ON p.id = sh.project_id AND p.workspace_id = sh.workspace_id
         WHERE sh.token = $1 AND sh.revoked_at IS NULL`,
		token,
	).Scan(&workspaceID, &projectID, &b.Workspace, &project)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("issueboard: resolve: %w", err)
	}
	if project != nil {
		b.Project = *project
	}

	rows, err := s.pool.Query(ctx,
		`SELECT identifier, title, status, priority, updated_at FROM issues
         WHERE workspace_id = $1 AND ($2::text IS NULL OR project_id = $2)
         ORDER BY updated_at DESC NULLS LAST, id
         LIMIT $3`,
		workspaceID, projectID, MaxBoardIssues+1,
	)
	if err != nil {
		return nil, fmt.Errorf("issueboard: issues: %w", err)
	}
	defer rows.Close()
	b.Issues = []BoardIssue{}
	for rows.Next() {
		var (
			it      BoardIssue
			updated *time.Time
		)
		if err := rows.Scan(&it.Identifier, &it.Title, &it.Status, &it.Priority, &updated); err != nil {
			return nil, err
		}
		if updated != nil {
			it.UpdatedAt = *updated
		}
		b.Issues = append(b.Issues, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(b.Issues) > MaxBoardIssues {
		b.Issues = b.Issues[:MaxBoardIssues]
		b.Truncated = true
	}
	return &b, nil
}
