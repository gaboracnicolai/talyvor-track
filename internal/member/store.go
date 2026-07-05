// Package member serves the service-authenticated members endpoint
// (GET /v1/service/members) that a Docs background sync pulls. It exposes the
// MINIMUM — (email, role, member_id) tuples — scoped to one workspace, behind a
// bearer service token. It never returns richer member PII (name/avatar/created_at).
package member

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkspaceMember is the projected membership tuple — deliberately the minimum the
// sync needs, NOT name/avatar_url/created_at.
type WorkspaceMember struct {
	Email    string `json:"email"`
	Role     string `json:"role"`
	MemberID string `json:"member_id"`
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ListWorkspaceMembers returns one workspace's members, projected to
// (email, role, member_id), ordered by email. Mirrors the mcp membersStore
// teamID=="" query (server.go), scoped strictly to workspaceID. limit/offset are
// clamped by the caller.
func (s *Store) ListWorkspaceMembers(ctx context.Context, workspaceID string, limit, offset int) ([]WorkspaceMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT email, role, id FROM members WHERE workspace_id = $1 ORDER BY email LIMIT $2 OFFSET $3`,
		workspaceID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("member: list workspace members: %w", err)
	}
	defer rows.Close()

	out := make([]WorkspaceMember, 0)
	for rows.Next() {
		var m WorkspaceMember
		if err := rows.Scan(&m.Email, &m.Role, &m.MemberID); err != nil {
			return nil, fmt.Errorf("member: scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
