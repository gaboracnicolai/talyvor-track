// Package member serves the service-authenticated members endpoint
// (GET /v1/service/members) that a Docs background sync pulls. It exposes the
// MINIMUM — (email, role, member_id) tuples — scoped to one workspace, behind a
// bearer service token. It never returns richer member PII (name/avatar/created_at).
//
// SECURITY POSTURE. The bearer token (config.MemberSyncSecret) authorizes reads of
// ANY workspace's roster — the caller is a trusted service principal, not a
// per-workspace member. Containment for that broad grant, not just forgery-resistance:
//   - constant-time bearer compare; unset secret ⇒ 401-refuses-all (see handler.go);
//   - every post-auth pull is audit-logged (workspace_id + COUNT, never the roster —
//     a log that copies emails would be a second leak) so a leaked-token
//     cross-workspace enumeration is visible;
//   - the operational secret contract (dedicated, server-side only, lockstep rotation)
//     is documented on config.MemberSyncSecret.
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

// ListWorkspaceIDs returns every workspace id on this deployment.
//
// Track is the tenancy source of truth — it mints a workspace per identity at login
// (internal/workspace/bootstrap.go) — so it is the only service that can answer this. It exists to
// break the Docs cold-start deadlock: Docs used to enumerate workspaces from its OWN content, so a
// workspace with no content was never synced a roster, so every write into it 403d, so it never got
// content. See workspaces.go for the compensating controls on the endpoint that exposes this.
func (s *Store) ListWorkspaceIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("member: enumerate workspaces: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("member: scan workspace id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
