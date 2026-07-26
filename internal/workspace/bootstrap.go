package workspace

// Bootstrap gives a signed-in identity its first workspace.
//
// ── WHY A SECOND CREATE PATH ────────────────────────────────────────────────
//
// POST /v1/workspaces already exists and already calls CreateWithOwner. It is
// USER-DRIVEN and unconditional: the client names the workspace and its slug, and
// every call makes another one. That is exactly right for "create a second
// workspace called Platform", and exactly wrong for login — where the caller
// supplies nothing, the same person arrives repeatedly, and the second arrival
// must find the first workspace instead of making another. The distinguishing
// property is idempotence, which is a different operation, not a flag on that one.
//
// ── WHY THIS IS A ROUTE AND NOT PART OF authz.Middleware ────────────────────
//
// The middleware is the obvious place: it already resolves the verified email to
// memberships, so "if there are none, make one" fits in four lines. It is
// deliberately NOT done there, and this is the decision most likely to be
// "simplified" later, so the reasoning lives here rather than in a commit message:
//
//   1. It would make a SECURITY CHOKEPOINT WRITE. Every request through the /v1
//      and /mcp chains — including every GET — would become a potential tenant
//      creation. Authorization code should decide, not mutate.
//   2. It would provision from a TYPO. Any email the gateway forwards would
//      create a workspace; a mistyped address becomes a permanent tenant that
//      nobody asked for and nobody owns.
//   3. It would RACE. Concurrent first requests (a page that fires three XHRs on
//      load) would each see zero memberships and each try to create.
//
// So provisioning happens when it is explicitly requested, once, at login.
// TestBootstrap_IsNotASideEffectOfReading is the standing guard.
//
// ── NO NEW SECRET ───────────────────────────────────────────────────────────
//
// Lens needed a secret-gated route because it had no identity concept at all.
// Track already has one: gatewayauth verifies X-Gateway-Auth constant-time BEFORE
// any identity header is trusted, and leaves the verified Identity in context.
// This route reads that and nothing else — no body, no query, no path parameter,
// and no new trust mechanism.
//
// ── THE PATH IS /v1/bootstrap, NOT /v1/workspaces/bootstrap ─────────────────
//
// authz.workspaceIDFromPath treats the third segment of /v1/workspaces/... as a
// {wsID}, so /v1/workspaces/bootstrap would be read as the workspace id
// "bootstrap", found in none of the caller's memberships, and 403'd before this
// handler ever ran. A caller with no workspace cannot pass a membership check by
// construction, so the route must live outside that prefix.

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/model"
)

// BootstrapResult is the response. Created distinguishes the login that made the
// workspace from every later one, so a caller can act once (a welcome screen, a
// first-run tour) without keeping its own state.
type BootstrapResult struct {
	WorkspaceID string `json:"workspace_id"`
	Slug        string `json:"slug"`
	Created     bool   `json:"created"`
}

// bootstrapSlug derives the idempotency key from the verified identity.
//
// KEYED ON (issuer, subject), NOT EMAIL. An email address can be reassigned to a
// different person; keying a tenant on it would eventually hand a new employee the
// previous holder's issues. The issuer is included because two IdPs can mint the
// same subject string, and a NUL separator makes the pair unambiguous so no
// split can be forged into another identity by string juggling. This mirrors the
// reasoning in Lens's provisionIdentity — deliberately mirrored rather than
// shared: Track derives its own key, in its own repo, and can change it without
// coordinating a release with two other products.
//
// The slug (not the id) is the key because workspaces.slug carries a UNIQUE
// constraint, which is what turns a check-then-create into something concurrency
// -safe. `id` defaults to a random uuid and cannot be a derived key without a
// schema change.
func bootstrapSlug(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return "w" + strings.ToLower(enc)[:26]
}

// errNoStableIdentity is returned when the gateway supplied no (issuer, subject).
var errNoStableIdentity = errors.New("bootstrap: verified identity carries no stable (issuer, subject)")

// Bootstrap returns the caller's workspace, creating it on first sight.
//
// IDEMPOTENCE IS THE CORRECTNESS PROPERTY and it is enforced twice: by looking the
// slug up first (the common path — one query on a second login), and by the
// database's UNIQUE(slug) when two logins race past that lookup together. The
// loser of a race re-reads rather than erroring, so N concurrent logins converge
// on one workspace with one owner.
func (h *Handler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	id, ok := gatewayauth.IdentityFrom(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no verified identity")
		return
	}
	// Fail closed rather than falling back to the reassignable field: a tenant
	// keyed on something that can change owner is worse than no tenant.
	if id.Issuer == "" || id.UserID == "" {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", errNoStableIdentity.Error())
		return
	}
	// The owner member row is email-keyed because Track's membership model is
	// (workspace_id, email). Without one the workspace would be unreachable to its
	// own creator, so this is required even though the KEY is the stable pair.
	if id.Email == "" {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "bootstrap: verified identity carries no email for the owner member")
		return
	}

	slug := bootstrapSlug(id.Issuer, id.UserID)

	// Already provisioned — the overwhelmingly common case after the first login.
	if existing, err := h.store.GetBySlug(r.Context(), slug); err == nil && existing != nil {
		writeJSON(w, http.StatusOK, BootstrapResult{WorkspaceID: existing.ID, Slug: existing.Slug})
		return
	}

	out, err := h.store.CreateWithOwner(r.Context(), model.Workspace{
		// No name claim reaches Track through the gateway, so the workspace gets a
		// neutral placeholder rather than an email in a display field. It is
		// renameable through the existing owner-gated PATCH.
		Name: "My workspace",
		Slug: slug,
	}, id.Email)
	if err != nil {
		// A UNIQUE(slug) violation means a concurrent login won the race between
		// the lookup above and this insert. That is success, not failure: re-read
		// and return the workspace the winner created.
		if isUniqueViolation(err) {
			if existing, gerr := h.store.GetBySlug(r.Context(), slug); gerr == nil && existing != nil {
				writeJSON(w, http.StatusOK, BootstrapResult{WorkspaceID: existing.ID, Slug: existing.Slug})
				return
			}
		}
		writeErr(w, http.StatusInternalServerError, "BOOTSTRAP_FAILED", "could not provision a workspace")
		return
	}
	writeJSON(w, http.StatusOK, BootstrapResult{WorkspaceID: out.ID, Slug: out.Slug, Created: true})
}

// isUniqueViolation reports a Postgres 23505. CreateWithOwner runs the workspace
// and owner inserts in ONE transaction, so a violation leaves neither behind —
// which is what makes a retry safe and is why a caller may re-attempt rather than
// cache the failure.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsUniqueViolationForTest exposes the predicate the race-recovery path depends on
// so a test can assert it recognises the error a duplicate slug actually produces.
// Exported for tests only; production code calls isUniqueViolation directly.
func IsUniqueViolationForTest(err error) bool { return isUniqueViolation(err) }
