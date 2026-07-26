package realtime

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Room-id grammar. Every room a Notifier publishes to is one of these three shapes
// (see notifier.go); anything else is not a room this server produces and is refused.
const (
	roomKindWorkspace = "workspace"
	roomKindTeam      = "team"
	roomKindIssue     = "issue"
)

// RoomAuthorizer answers "may a client authorized for workspaceID join this room?" for
// the object-keyed room kinds. Injected so the hub stays free of a database import and
// tests can substitute a decision table.
//
// It is only consulted for team: and issue: rooms — workspace: is decided by string
// comparison against the client's own workspace and never needs a query.
type RoomAuthorizer interface {
	AuthorizeRoom(ctx context.Context, workspaceID, kind, objectID string) (bool, error)
}

// PGRoomAuthorizer resolves room ownership against Postgres.
type PGRoomAuthorizer struct{ pool *pgxpool.Pool }

func NewPGRoomAuthorizer(pool *pgxpool.Pool) *PGRoomAuthorizer { return &PGRoomAuthorizer{pool: pool} }

// roomOwnershipSQL are FIXED literals per kind (no dynamic table name, no injection
// surface) mirroring the pattern in internal/tenancy: does this object live in this
// workspace?
var roomOwnershipSQL = map[string]string{
	roomKindTeam:  `SELECT EXISTS(SELECT 1 FROM teams  WHERE id = $1 AND workspace_id = $2)`,
	roomKindIssue: `SELECT EXISTS(SELECT 1 FROM issues WHERE id = $1 AND workspace_id = $2)`,
}

func (a *PGRoomAuthorizer) AuthorizeRoom(ctx context.Context, workspaceID, kind, objectID string) (bool, error) {
	if a == nil || a.pool == nil {
		return false, errors.New("realtime: room authorizer has no pool")
	}
	q, ok := roomOwnershipSQL[kind]
	if !ok {
		return false, nil // unknown kind ⇒ deny; a new room kind must opt in here
	}
	var owned bool
	if err := a.pool.QueryRow(ctx, q, objectID, workspaceID).Scan(&owned); err != nil {
		return false, err
	}
	return owned, nil
}

// WithRoomAuthorizer attaches the room-ownership resolver. Call during setup, before Run.
//
// LEAVING IT UNSET IS SAFE, NOT PERMISSIVE: without an authorizer the hub can only prove
// ownership of workspace: rooms (a string compare), so team: and issue: subscriptions are
// DENIED. A nil authorizer degrades the feature, it does not open the boundary.
func (h *Hub) WithRoomAuthorizer(a RoomAuthorizer) *Hub {
	h.rooms4uth = a
	return h
}

// splitRoom parses "<kind>:<objectID>". Returns ok=false for anything that is not exactly
// one colon with non-empty sides, so malformed ids are refused rather than guessed at.
func splitRoom(roomID string) (kind, objectID string, ok bool) {
	i := strings.IndexByte(roomID, ':')
	if i <= 0 || i == len(roomID)-1 {
		return "", "", false
	}
	kind, objectID = roomID[:i], roomID[i+1:]
	if strings.ContainsRune(objectID, ':') {
		return "", "", false
	}
	return kind, objectID, true
}

// authorizeRoom is the single chokepoint deciding whether client c may join roomID.
//
// AUDIT FINDING 7: Subscribe previously took any room id and created the room map on
// demand, with no tenancy check at all. Rooms carry the full model.Issue and full
// model.Comment, and room names are not secret — the anonymous public feature board hands
// out workspace_id. So an authenticated member of any workspace could subscribe to
// another tenant's room and read its live issue and comment stream. It was unreachable
// only because /v1/ws 403'd everyone (finding 3); fixing that armed this, which is why
// both land together.
//
// Fail-closed on every branch: unparseable id, unknown kind, no authorizer for an
// object-keyed room, or a lookup error all deny.
func (h *Hub) authorizeRoom(ctx context.Context, c *Client, roomID string) bool {
	kind, objectID, ok := splitRoom(roomID)
	if !ok {
		return false
	}
	switch kind {
	case roomKindWorkspace:
		// Decided without a query: the room IS the workspace.
		return objectID == c.WorkspaceID
	case roomKindTeam, roomKindIssue:
		if h.rooms4uth == nil {
			return false // cannot prove ownership ⇒ refuse
		}
		owned, err := h.rooms4uth.AuthorizeRoom(ctx, c.WorkspaceID, kind, objectID)
		return err == nil && owned
	default:
		return false
	}
}
