// Package notification owns the notifications table. Notifications
// survive process restarts (unlike in-memory presence) so the bell
// icon shows what a user missed while disconnected.
package notification

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

type Notification struct {
	ID          string    `json:"id"                 db:"id"`
	WorkspaceID string    `json:"workspace_id"       db:"workspace_id"`
	MemberID    string    `json:"member_id"          db:"member_id"`
	Type        string    `json:"type"               db:"type"`
	Title       string    `json:"title"              db:"title"`
	Body        string    `json:"body"               db:"body"`
	IssueID     *string   `json:"issue_id,omitempty" db:"issue_id"`
	Read        bool      `json:"read"               db:"read"`
	CreatedAt   time.Time `json:"created_at"         db:"created_at"`
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

const columns = `id, workspace_id, member_id, type, title, body, issue_id, read, created_at`

func scan(s interface{ Scan(...any) error }) (*Notification, error) {
	var n Notification
	if err := s.Scan(&n.ID, &n.WorkspaceID, &n.MemberID, &n.Type, &n.Title, &n.Body,
		&n.IssueID, &n.Read, &n.CreatedAt); err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Store) Create(ctx context.Context, n Notification) (*Notification, error) {
	if n.WorkspaceID == "" || n.MemberID == "" || n.Type == "" || n.Title == "" {
		return nil, errors.New("notification: WorkspaceID, MemberID, Type, Title required")
	}
	if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "members", n.MemberID, n.WorkspaceID); err != nil {
		return nil, err
	}
	if n.IssueID != nil && *n.IssueID != "" {
		if err := tenancy.AssertRefInWorkspace(ctx, s.pool, "issues", *n.IssueID, n.WorkspaceID); err != nil {
			return nil, err
		}
	}
	return scan(s.pool.QueryRow(ctx,
		`INSERT INTO notifications (workspace_id, member_id, type, title, body, issue_id)
        VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+columns,
		n.WorkspaceID, n.MemberID, n.Type, n.Title, n.Body, n.IssueID,
	))
}

// List returns notifications for a member. Unread always come first
// (sorted newest within the unread set), then read notifications
// follow in reverse-chronological order.
func (s *Store) List(ctx context.Context, memberID string, unreadOnly bool, limit int) ([]Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var (
		rows pgx.Rows
		err  error
	)
	if unreadOnly {
		rows, err = s.pool.Query(ctx,
			`SELECT `+columns+` FROM notifications
            WHERE member_id = $1 AND read = false
            ORDER BY created_at DESC LIMIT $2`,
			memberID, limit,
		)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT `+columns+` FROM notifications
            WHERE member_id = $1
            ORDER BY read ASC, created_at DESC LIMIT $2`,
			memberID, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("notification: list: %w", err)
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func (s *Store) MarkRead(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications SET read = true WHERE id = $1`, id)
	return err
}

// MarkAllRead clears every unread notification for a member. Used by
// the "clear all" button in the notifications dropdown.
func (s *Store) MarkAllRead(ctx context.Context, memberID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE notifications SET read = true WHERE member_id = $1 AND read = false`,
		memberID,
	)
	return err
}
