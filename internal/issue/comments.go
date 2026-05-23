package issue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/talyvor/track/internal/model"
)

const commentColumns = `id, issue_id, author_id, body, edited_at, created_at, updated_at`

func scanComment(s interface{ Scan(...any) error }) (*model.Comment, error) {
	var c model.Comment
	if err := s.Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.Body, &c.EditedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateComment appends a comment to an issue. AuthorID is the
// member's ID; comments are NEVER soft-deleted (unlike issues) —
// users can delete their own comments outright.
func (s *Store) CreateComment(ctx context.Context, c model.Comment) (*model.Comment, error) {
	if c.IssueID == "" || c.AuthorID == "" || c.Body == "" {
		return nil, errors.New("comment: IssueID, AuthorID, and Body required")
	}
	return scanComment(s.pool.QueryRow(ctx,
		`INSERT INTO comments (issue_id, author_id, body)
        VALUES ($1, $2, $3) RETURNING `+commentColumns,
		c.IssueID, c.AuthorID, c.Body,
	))
}

// ListComments returns every comment for an issue, oldest first.
// The conversation flow is chronological; new comments appear at the
// bottom of the thread.
func (s *Store) ListComments(ctx context.Context, issueID string) ([]model.Comment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+commentColumns+` FROM comments WHERE issue_id = $1 ORDER BY created_at ASC`,
		issueID,
	)
	if err != nil {
		return nil, fmt.Errorf("comment: list: %w", err)
	}
	defer rows.Close()
	var out []model.Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// UpdateComment changes the body and stamps edited_at so the UI can
// show a "(edited)" badge. Only the body is editable — author, issue,
// and timestamps are immutable.
func (s *Store) UpdateComment(ctx context.Context, id, body string) (*model.Comment, error) {
	if body == "" {
		return nil, errors.New("comment: body required")
	}
	return scanComment(s.pool.QueryRow(ctx,
		`UPDATE comments SET body = $2, edited_at = $3, updated_at = NOW()
        WHERE id = $1 RETURNING `+commentColumns,
		id, body, time.Now().UTC(),
	))
}

// DeleteComment is a hard delete. Issues are soft-cancelled to keep
// identifiers stable; comments don't carry identifiers so dropping
// them outright is fine. The CASCADE on the issues FK takes care of
// orphans if the parent issue is removed.
func (s *Store) DeleteComment(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM comments WHERE id = $1`, id)
	return err
}

// silence unused import warning during the build dance — pgx itself
// is used via the pool interface defined in store.go.
var _ = pgx.ErrNoRows
