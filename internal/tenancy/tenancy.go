// Package tenancy is the single cross-object workspace guard for Track.
//
// Every store that inserts or sets a *_id reference to another object alongside a
// workspace_id must route that reference through AssertRefInWorkspace, so a caller
// can never link an object to a reference in a different workspace. This is the one
// primitive the object-graph cross-object tenancy class is closed with; a CI
// regression lock (.semgrep/cross-object-tenancy.yml) fails the build when a
// cross-object insert skips it.
package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrCrossWorkspace is the typed error returned when a referenced object does not
// belong to the expected workspace (or does not exist). Callers may errors.Is it.
var ErrCrossWorkspace = errors.New("tenancy: cross-workspace reference")

// Querier is the minimal surface satisfied by both *pgxpool.Pool and pgx.Tx, so the
// guard works on the pool or inside a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// refQueries holds the per-table EXISTS guard, keyed by an allowlisted,
// workspace-scoped table name. The queries are string LITERALS (no dynamic table
// name interpolated into SQL), so there is no injection surface and the set of
// reference tables is explicit and auditable. Add a table here to make it a valid
// cross-object reference target.
var refQueries = map[string]string{
	"issues":          `SELECT EXISTS(SELECT 1 FROM issues WHERE id = $1 AND workspace_id = $2)`,
	"projects":        `SELECT EXISTS(SELECT 1 FROM projects WHERE id = $1 AND workspace_id = $2)`,
	"teams":           `SELECT EXISTS(SELECT 1 FROM teams WHERE id = $1 AND workspace_id = $2)`,
	"members":         `SELECT EXISTS(SELECT 1 FROM members WHERE id = $1 AND workspace_id = $2)`,
	"cycles":          `SELECT EXISTS(SELECT 1 FROM cycles WHERE id = $1 AND workspace_id = $2)`,
	"feature_boards":  `SELECT EXISTS(SELECT 1 FROM feature_boards WHERE id = $1 AND workspace_id = $2)`,
	"issue_templates": `SELECT EXISTS(SELECT 1 FROM issue_templates WHERE id = $1 AND workspace_id = $2)`,
}

// AssertRefInWorkspace verifies that the row identified by refID in refTable has
// workspace_id == workspaceID. It returns a wrapped ErrCrossWorkspace when the
// referenced row is in another workspace or does not exist. refTable MUST be one of
// the allowlisted, compile-time table names in refQueries — never user input.
func AssertRefInWorkspace(ctx context.Context, q Querier, refTable, refID, workspaceID string) error {
	query, ok := refQueries[refTable]
	if !ok {
		return fmt.Errorf("tenancy: no guard registered for table %q (programmer error)", refTable)
	}
	var exists bool
	if err := q.QueryRow(ctx, query, refID, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("tenancy: checking %s %q: %w", refTable, refID, err)
	}
	if !exists {
		return fmt.Errorf("%w: %s %q is not in workspace %q", ErrCrossWorkspace, refTable, refID, workspaceID)
	}
	return nil
}
