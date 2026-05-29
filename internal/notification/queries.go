package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/email"
	"github.com/talyvor/track/internal/model"
)

// dbDirectory is the production directory backed by the issues/comments/
// members tables. All queries are read-only and run off the request path's
// write transaction.
type dbDirectory struct {
	pool pgxDB
}

func newDBDirectory(pool *pgxpool.Pool) *dbDirectory {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return &dbDirectory{pool: db}
}

func (d *dbDirectory) MembersByIDs(ctx context.Context, ids []string) (map[string]model.Member, error) {
	out := make(map[string]model.Member, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx,
		`SELECT id, workspace_id, name, email, avatar_url, role, created_at
         FROM members WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("notification: members by ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m model.Member
		if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.Name, &m.Email, &m.AvatarURL, &m.Role, &m.CreatedAt); err != nil {
			return nil, err
		}
		out[m.ID] = m
	}
	return out, rows.Err()
}

// IssueParticipants returns the creator, current assignee, and everyone who has
// commented on the issue — Track's implicit "watchers". (Track has no explicit
// watch table; participation is the watcher signal.)
func (d *dbDirectory) IssueParticipants(ctx context.Context, issueID string) ([]string, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT creator_id FROM issues WHERE id = $1
         UNION
         SELECT assignee_id FROM issues WHERE id = $1 AND assignee_id IS NOT NULL
         UNION
         SELECT author_id FROM comments WHERE issue_id = $1`, issueID)
	if err != nil {
		return nil, fmt.Errorf("notification: participants: %w", err)
	}
	defer rows.Close()
	return scanIDs(rows)
}

// SprintMembers returns the distinct assignees of issues in the cycle. (Track
// has no team_members table; cycle participation is derived from assignment.)
func (d *dbDirectory) SprintMembers(ctx context.Context, cycleID string) ([]string, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT DISTINCT assignee_id FROM issues
         WHERE cycle_id = $1 AND assignee_id IS NOT NULL`, cycleID)
	if err != nil {
		return nil, fmt.Errorf("notification: sprint members: %w", err)
	}
	defer rows.Close()
	return scanIDs(rows)
}

// ResolveMentions maps @handles to member IDs in the workspace, matching the
// email local-part or the name with spaces removed (case-insensitive). See the
// note in mentions.go on why this heuristic is used.
func (d *dbDirectory) ResolveMentions(ctx context.Context, workspaceID string, handles []string) ([]string, error) {
	if len(handles) == 0 {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx,
		`SELECT id FROM members
         WHERE workspace_id = $1
           AND (lower(split_part(email, '@', 1)) = ANY($2)
                OR lower(replace(name, ' ', '')) = ANY($2))`,
		workspaceID, handles)
	if err != nil {
		return nil, fmt.Errorf("notification: resolve mentions: %w", err)
	}
	defer rows.Close()
	return scanIDs(rows)
}

func (d *dbDirectory) LoadIssue(ctx context.Context, issueID string) (*IssueRef, error) {
	var r IssueRef
	err := d.pool.QueryRow(ctx,
		`SELECT id, workspace_id, identifier, title, creator_id, assignee_id
         FROM issues WHERE id = $1`, issueID,
	).Scan(&r.ID, &r.WorkspaceID, &r.Identifier, &r.Title, &r.CreatorID, &r.AssigneeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errNotFound
		}
		return nil, fmt.Errorf("notification: load issue: %w", err)
	}
	return &r, nil
}

func scanIDs(rows pgx.Rows) ([]string, error) {
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// NewDispatcher builds the production dispatcher wired to the database. Returns
// nil if renderer is nil (email disabled) so callers can leave the hook unset.
func NewDispatcher(pool *pgxpool.Pool, prefs *PreferenceStore, queue enqueuer, renderer *email.Renderer, baseURL, appName string, logger *slog.Logger) *Dispatcher {
	return newDispatcher(newDBDirectory(pool), prefs, queue, renderer, baseURL, appName, logger)
}
