// Package authz is T10 — the workspace authorization layer that sits on top of T9's
// transit-proof boundary. T9 proved WHO the caller is (verified email in context); authz
// resolves that email to the workspaces the caller is a member of and, for a request
// carrying a path {wsID}, authorizes it: the {wsID} must be one of the caller's
// workspaces or the request is refused 403. The server-AUTHORIZED workspace_id and the
// caller's member.id there are placed in context; handlers read those, never the
// caller-supplied URL param or X-Member-Id. This is the IDOR cure: the workspace in every
// store filter comes from membership, not from the URL.
package authz

import (
	"context"
	"net/http"
	"strings"

	"github.com/talyvor/track/internal/gatewayauth"
)

// Membership is one (workspace, member, role) the verified caller belongs to.
type Membership struct {
	WorkspaceID string
	MemberID    string
	Role        string
}

// Resolver resolves a gateway-verified email to its memberships. The PG impl queries the
// members table (internal/authz/resolver.go); tests inject a fake.
type Resolver interface {
	MembershipsByEmail(ctx context.Context, email string) ([]Membership, error)
}

type ctxKey struct{}

type authCtx struct {
	workspaceID  string // server-AUTHORIZED workspace — set ONLY on a {wsID} route the caller is a member of
	memberID     string // the caller's member.id in that workspace (the resolved actor)
	role         string
	hasWorkspace bool
	memberships  []Membership
}

// WorkspaceID returns the server-AUTHORIZED workspace id. ok=false when none was
// authorized (no {wsID} on the route, or the request never passed the boundary). A
// handler that needs a workspace MUST treat ok=false as unauthorized — the value is set
// only after the membership check passed, so it can never be a caller-chosen id. This is
// the fail-closed property: there is no way to read an unauthorized workspace.
func WorkspaceID(ctx context.Context) (string, bool) {
	ac, ok := ctx.Value(ctxKey{}).(*authCtx)
	if !ok || !ac.hasWorkspace {
		return "", false
	}
	return ac.workspaceID, true
}

// MemberID returns the resolved actor — the caller's member.id in the authorized
// workspace. ok=false when no workspace was authorized. Replaces the spoofable
// X-Member-Id header.
func MemberID(ctx context.Context) (string, bool) {
	ac, ok := ctx.Value(ctxKey{}).(*authCtx)
	if !ok || !ac.hasWorkspace {
		return "", false
	}
	return ac.memberID, true
}

// Memberships returns the verified caller's full membership set — used by no-{wsID}
// routes (GET /v1/workspaces) to scope a list to the caller's own workspaces. ok=false
// when the request never passed the boundary.
func Memberships(ctx context.Context) ([]Membership, bool) {
	ac, ok := ctx.Value(ctxKey{}).(*authCtx)
	if !ok {
		return nil, false
	}
	return ac.memberships, true
}

// AuthorizeWorkspace authorizes a CALLER-SUPPLIED workspace id (from a query param, a tool
// argument, a body field — any route where the path did not carry {wsID}) against the
// verified caller's resolved memberships. It returns the matching Membership (so the caller
// gets the resolved member.id as the actor) and ok=false when the caller is not a member.
// This is the single place the "is the caller a member of THIS workspace" rule lives — the
// importer uses it for ?workspace_id=, and the MCP tools (T11b) will reuse it for their
// workspace_id argument. Fail-closed: an empty id, or no memberships in context (the
// request never passed T9/T10), → ok=false.
func AuthorizeWorkspace(ctx context.Context, workspaceID string) (Membership, bool) {
	if workspaceID == "" {
		return Membership{}, false
	}
	ms, ok := Memberships(ctx)
	if !ok {
		return Membership{}, false
	}
	return membershipFor(ms, workspaceID)
}

// WithAuthorized returns a context carrying an authorized workspace + the caller's member
// id there. The middleware installs this after the membership check passes; handler tests
// use it to exercise a {wsID} handler without standing up the full middleware chain.
func WithAuthorized(ctx context.Context, workspaceID, memberID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, &authCtx{
		workspaceID:  workspaceID,
		memberID:     memberID,
		hasWorkspace: workspaceID != "",
	})
}

// WithMemberships returns a context carrying just the membership set (no authorized
// workspace) — for no-{wsID} routes (GET /v1/workspaces) and their tests.
func WithMemberships(ctx context.Context, ms []Membership) context.Context {
	return context.WithValue(ctx, ctxKey{}, &authCtx{memberships: ms})
}

// Member-tier roles. Two tiers only: an owner can perform the elevated operations
// (delete/administer the workspace, write integration secrets); everyone else is a
// member. Stored free-text in members.role (no DB CHECK); the closed set is enforced
// here and on the member-management write paths.
const (
	RoleOwner  = "owner"
	RoleMember = "member"
)

// Role returns the caller's role in the server-AUTHORIZED workspace. ok=false when no
// {wsID} workspace was authorized (a no-{wsID}/flat route, or a request that never
// passed the boundary) OR the resolved role is empty — the middleware only sets a role
// on a {wsID} route the caller is a member of (see Middleware). Flat routes that hold a
// Membership (e.g. integrations via AuthorizeWorkspace) use IsOwnerRole on that instead.
func Role(ctx context.Context) (string, bool) {
	ac, ok := ctx.Value(ctxKey{}).(*authCtx)
	if !ok || !ac.hasWorkspace || ac.role == "" {
		return "", false
	}
	return ac.role, true
}

// IsOwner reports whether the caller is an owner of the authorized workspace. FAIL-CLOSED:
// no authorized workspace, an empty role, or any unrecognised role → false. This is the
// gate the elevated {wsID} operations call.
func IsOwner(ctx context.Context) bool {
	r, ok := Role(ctx)
	return ok && IsOwnerRole(r)
}

// IsOwnerRole is the exact-match owner test for a role STRING — used by flat routes that
// resolved a Membership via AuthorizeWorkspace (which never sets the ctx role). Exact and
// case-sensitive: only "owner" is owner; "", "member", "admin", "OWNER" are not.
func IsOwnerRole(role string) bool { return role == RoleOwner }

// WithAuthorizedRole is WithAuthorized plus the resolved role — the middleware installs
// this for {wsID} routes so IsOwner has a value to read, and handler tests use it to
// exercise the owner gate without standing up the full chain. Kept SEPARATE from
// WithAuthorized (rather than adding a param) so existing callers stay byte-stable.
func WithAuthorizedRole(ctx context.Context, workspaceID, memberID, role string) context.Context {
	return context.WithValue(ctx, ctxKey{}, &authCtx{
		workspaceID:  workspaceID,
		memberID:     memberID,
		role:         role,
		hasWorkspace: workspaceID != "",
	})
}

// RequireOwner is the middleware form of the owner gate (the member analogue of guest
// RequireGuest): it 403s any request whose authorized-workspace role is not owner. Reads
// the role the {wsID} middleware already resolved; fail-closed via IsOwner.
func RequireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsOwner(r.Context()) {
			forbiddenOwner(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func forbiddenOwner(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"owner role required","code":"OWNER_REQUIRED"}`))
}

// Middleware resolves the T9-verified identity to memberships and, for a request carrying
// a path {wsID}, AUTHORIZES it (the {wsID} must be a workspace the caller is a member of,
// else 403), placing the authorized workspace_id + the caller's member.id there into
// context. A no-{wsID} route gets the membership set only (no authorized workspace).
// exempt(path) mirrors T9 — own-auth paths carry no verified identity and are skipped.
func Middleware(resolver Resolver, exempt func(path string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt != nil && exempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// T9 must have run and verified the caller. No identity on a protected route
			// → cannot authorize → refuse (this also covers the email being empty).
			id, ok := gatewayauth.IdentityFrom(r.Context())
			if !ok || id.Email == "" {
				forbidden(w)
				return
			}
			memberships, err := resolver.MembershipsByEmail(r.Context(), id.Email)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"membership resolution failed","code":"AUTHZ_ERROR"}`))
				return
			}

			ac := &authCtx{memberships: memberships}
			// {wsID} present → authorize it against membership; absent → no authorized
			// workspace (the no-{wsID} routes use Memberships()).
			if wsID := workspaceIDFromPath(r.URL.Path); wsID != "" {
				m, found := membershipFor(memberships, wsID)
				if !found {
					forbidden(w) // verified caller is not a member of the requested workspace
					return
				}
				ac.workspaceID, ac.memberID, ac.role, ac.hasWorkspace = m.WorkspaceID, m.MemberID, m.Role, true
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ac)))
		})
	}
}

// workspaceIDFromPath extracts {wsID} from a /v1/workspaces/{wsID}/... path. It reads the
// path directly because a chi mux-level middleware runs BEFORE the sub-mux populates URL
// params, so chi.URLParam would be empty here. Returns "" for paths without a workspace
// id (e.g. /v1/workspaces list/create). A bogus or manipulated value cannot bypass auth —
// it simply won't match any membership → 403. Every workspace-scoped /v1 route is uniform
// (/v1/workspaces/{wsID}/...), so this is the single source of the acted-on workspace.
func workspaceIDFromPath(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) >= 3 && parts[0] == "v1" && parts[1] == "workspaces" {
		return parts[2] // "" for "/v1/workspaces" or "/v1/workspaces/" (the list/create routes)
	}
	return ""
}

func membershipFor(ms []Membership, wsID string) (Membership, bool) {
	for _, m := range ms {
		if m.WorkspaceID == wsID {
			return m, true
		}
	}
	return Membership{}, false
}

func forbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"not a member of this workspace","code":"WORKSPACE_FORBIDDEN"}`))
}
