package authz

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGResolver resolves memberships from the members table. It matches the email lower-cased
// and trimmed (idx_members_email_canonical, migration 0030), so "Ann@X.com" and "ann@x.com"
// are the same member; the stored address stays as it was typed. It returns ONE row per
// workspace — the (workspace_id, member.id, role) tuple the middleware authorizes against.
// Where a workspace holds two rows that differ only by case or spaces (stored before this
// rule; none are merged automatically), the exact spelling wins, then an owner row, then the
// oldest, so nobody who could sign in before loses the membership they had.
type PGResolver struct{ pool *pgxpool.Pool }

func NewPGResolver(pool *pgxpool.Pool) *PGResolver { return &PGResolver{pool: pool} }

func (r *PGResolver) MembershipsByEmail(ctx context.Context, email string) ([]Membership, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT ON (m.workspace_id) m.workspace_id, m.id, m.role, w.deleted_at IS NOT NULL
		   FROM members m JOIN workspaces w ON w.id = m.workspace_id
		  WHERE lower(btrim(m.email)) = lower(btrim($1))
		  ORDER BY m.workspace_id, (m.email = $1) DESC, (m.role = 'owner') DESC, m.created_at, m.id`, email)
	if err != nil {
		return nil, fmt.Errorf("authz: memberships by email: %w", err)
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.WorkspaceID, &m.MemberID, &m.Role, &m.Deleted); err != nil {
			return nil, fmt.Errorf("authz: scan membership: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
