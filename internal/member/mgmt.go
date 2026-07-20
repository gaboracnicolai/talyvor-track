package member

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/model"
)

// Sentinel errors the handler maps to HTTP status. Kept as values (errors.Is) so the
// store owns the invariant and the handler owns the wire shape.
var (
	ErrMemberExists   = errors.New("member: email is already a member of this workspace")
	ErrMemberNotFound = errors.New("member: not found in this workspace")
	ErrLastOwner      = errors.New("member: refusing to remove or demote the last owner of a workspace")
	ErrInvalidRole    = errors.New("member: role must be 'owner' or 'member'")
)

// ValidRole reports whether role is one of the two member tiers. The add/change paths
// reject anything else so members.role can never drift outside {owner, member} via the
// API (the DB column itself is free-text with no CHECK).
func ValidRole(role string) bool {
	return role == authz.RoleOwner || role == authz.RoleMember
}

const mgmtColumns = `id, workspace_id, name, email, avatar_url, role, created_at`

func scanMgmtMember(row pgx.Row) (*model.Member, error) {
	var m model.Member
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.Name, &m.Email, &m.AvatarURL, &m.Role, &m.CreatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

// ListMembers returns every member of workspaceID as full rows, ordered by email. Unlike
// the service projection (ListWorkspaceMembers), this is the in-workspace management view.
func (s *Store) ListMembers(ctx context.Context, workspaceID string) ([]model.Member, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+mgmtColumns+` FROM members WHERE workspace_id = $1 ORDER BY email`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("member: list members: %w", err)
	}
	defer rows.Close()
	out := make([]model.Member, 0)
	for rows.Next() {
		m, err := scanMgmtMember(rows)
		if err != nil {
			return nil, fmt.Errorf("member: scan: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// AddMember inserts a member into workspaceID with an EXPLICIT role — the INSERT always
// names the role and never relies on the DB default (lockout hazard a). name defaults to
// the email (the gateway carries no name claim, exactly as workspace.CreateWithOwner
// does). A UNIQUE(workspace_id, email) collision returns ErrMemberExists; an off-tier
// role returns ErrInvalidRole.
func (s *Store) AddMember(ctx context.Context, workspaceID, email, role string) (*model.Member, error) {
	if !ValidRole(role) {
		return nil, ErrInvalidRole
	}
	m, err := scanMgmtMember(s.pool.QueryRow(ctx,
		`INSERT INTO members (workspace_id, name, email, role)
         VALUES ($1, $2, $2, $3) RETURNING `+mgmtColumns,
		workspaceID, email, role))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return nil, ErrMemberExists
		}
		return nil, fmt.Errorf("member: add: %w", err)
	}
	return m, nil
}

// ChangeRole updates a member's role, scoped by (id, workspace_id). Refuses to demote the
// LAST owner (lockout hazard c). The owner count is taken under a row lock on the current
// owner rows so two concurrent demotions can't race a workspace down to zero owners.
func (s *Store) ChangeRole(ctx context.Context, workspaceID, memberID, role string) (*model.Member, error) {
	if !ValidRole(role) {
		return nil, ErrInvalidRole
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var curRole string
	err = tx.QueryRow(ctx,
		`SELECT role FROM members WHERE id = $1 AND workspace_id = $2`,
		memberID, workspaceID).Scan(&curRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("member: change role load: %w", err)
	}
	// Demoting an owner away from owner is only allowed if another owner remains.
	if curRole == authz.RoleOwner && role != authz.RoleOwner {
		n, err := lockedOwnerCount(ctx, tx, workspaceID)
		if err != nil {
			return nil, err
		}
		if n <= 1 {
			return nil, ErrLastOwner
		}
	}
	m, err := scanMgmtMember(tx.QueryRow(ctx,
		`UPDATE members SET role = $3 WHERE id = $1 AND workspace_id = $2 RETURNING `+mgmtColumns,
		memberID, workspaceID, role))
	if err != nil {
		return nil, fmt.Errorf("member: change role: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// RemoveMember deletes a member scoped by (id, workspace_id). Refuses to remove the LAST
// owner (lockout hazard b), under the same owner-row lock as ChangeRole.
func (s *Store) RemoveMember(ctx context.Context, workspaceID, memberID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var curRole string
	err = tx.QueryRow(ctx,
		`SELECT role FROM members WHERE id = $1 AND workspace_id = $2`,
		memberID, workspaceID).Scan(&curRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMemberNotFound
	}
	if err != nil {
		return fmt.Errorf("member: remove load: %w", err)
	}
	if curRole == authz.RoleOwner {
		n, err := lockedOwnerCount(ctx, tx, workspaceID)
		if err != nil {
			return err
		}
		if n <= 1 {
			return ErrLastOwner
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM members WHERE id = $1 AND workspace_id = $2`, memberID, workspaceID); err != nil {
		return fmt.Errorf("member: remove: %w", err)
	}
	return tx.Commit(ctx)
}

// lockedOwnerCount counts the workspace's owners while holding a row lock on each owner
// row (FOR UPDATE), so a concurrent demote/remove of a different owner serialises behind
// this transaction instead of both observing the pre-change count.
func lockedOwnerCount(ctx context.Context, tx pgx.Tx, workspaceID string) (int, error) {
	rows, err := tx.Query(ctx,
		`SELECT 1 FROM members WHERE workspace_id = $1 AND role = $2 FOR UPDATE`,
		workspaceID, authz.RoleOwner)
	if err != nil {
		return 0, fmt.Errorf("member: count owners: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("member: count owners: %w", err)
	}
	return n, nil
}
