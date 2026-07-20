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
//
// SEC (tenancy): the comment lands ONLY if its parent issue is in the
// caller's SERVER-authorized workspace. comments carry no workspace_id
// column (by design — scope via the parent issue), so the INSERT is
// gated by an EXISTS on issues(id, workspace_id): a foreign or
// nonexistent issue inserts 0 rows → ErrNotFound (403 ≡ 404, no
// existence oracle), mirroring ListComments/UpdateComment/DeleteComment.
func (s *Store) CreateComment(ctx context.Context, c model.Comment, workspaceID string) (*model.Comment, error) {
	if c.IssueID == "" || c.AuthorID == "" || c.Body == "" {
		return nil, errors.New("comment: IssueID, AuthorID, and Body required")
	}
	out, err := scanComment(s.pool.QueryRow(ctx,
		`INSERT INTO comments (issue_id, author_id, body)
        SELECT $1, $2, $3
        WHERE EXISTS (SELECT 1 FROM issues WHERE id = $1 AND workspace_id = $4)
        RETURNING `+commentColumns,
		c.IssueID, c.AuthorID, c.Body, workspaceID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return out, err
}

// ListComments returns every comment for an issue, oldest first.
// The conversation flow is chronological; new comments appear at the
// bottom of the thread.
func (s *Store) ListComments(ctx context.Context, issueID, workspaceID string) ([]model.Comment, error) {
	// SEC-5: comments carry no workspace_id — scope via the parent issue so a foreign issue's
	// comments are never enumerated (empty result for an out-of-workspace issue id).
	rows, err := s.pool.Query(ctx,
		`SELECT `+commentColumns+` FROM comments
        WHERE issue_id = $1 AND issue_id IN (SELECT id FROM issues WHERE workspace_id = $2)
        ORDER BY created_at ASC`,
		issueID, workspaceID,
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
// UpdateComment / DeleteComment are SEC-5 scoped via the comment's issue: comments carry no
// workspace_id, so the id is gated by `issue_id IN (SELECT id FROM issues WHERE workspace_id=$n)`.
// A comment on another workspace's issue → ErrNotFound (reuses the issue-package sentinel).
// UpdateComment / DeleteComment additionally enforce author-or-owner: only the comment's
// author, or a workspace OWNER (isOwner), may edit/delete it — `AND (author_id = $caller OR
// $isOwner)`. A non-author non-owner matches 0 rows → ErrNotFound (no-oracle: same as a
// foreign/absent comment). callerID is the server-resolved member id, never a client field.
func (s *Store) UpdateComment(ctx context.Context, id, workspaceID, callerID, body string, isOwner bool) (*model.Comment, error) {
	if body == "" {
		return nil, errors.New("comment: body required")
	}
	c, err := scanComment(s.pool.QueryRow(ctx,
		`UPDATE comments SET body = $2, edited_at = $3, updated_at = NOW()
        WHERE id = $1 AND issue_id IN (SELECT id FROM issues WHERE workspace_id = $4)
          AND (author_id = $5 OR $6)
        RETURNING `+commentColumns,
		id, body, time.Now().UTC(), workspaceID, callerID, isOwner,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) DeleteComment(ctx context.Context, id, workspaceID, callerID string, isOwner bool) error {
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM comments WHERE id = $1 AND issue_id IN (SELECT id FROM issues WHERE workspace_id = $2)
          AND (author_id = $3 OR $4)`,
		id, workspaceID, callerID, isOwner,
	)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// silence unused import warning during the build dance — pgx itself
// is used via the pool interface defined in store.go.
var _ = pgx.ErrNoRows
