package member

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// workspaces.go — GET /v1/service/workspaces. Track is the tenancy source of truth, so it is the
// only service that can answer "which workspaces exist on this deployment".
//
// WHY IT EXISTS: THE DOCS COLD-START DEADLOCK. Docs enumerated the workspaces to sync rosters for
// from its own content — `SELECT workspace_id FROM spaces UNION SELECT workspace_id FROM pages`.
// A workspace with no content is never enumerated, so it never gets a roster, so every write into
// it 403s for want of a membership row, so it never gets content. A brand-new tenant could not
// create their first page, ever. Inverting the source of enumeration is the whole fix: Docs asks
// the service that knows a workspace exists, instead of asking whether it has already been used.
//
// ⚠ THIS IS A FULL-DEPLOYMENT DUMP, and its sibling deliberately is not: GET /service/members
// refuses to run without an explicit workspace_id, precisely so a valid token cannot read every
// roster at once. The difference is not an oversight — enumeration IS the request here. Because
// the usual "scope it" control cannot apply, three others carry the weight instead, each pinned by
// a test in workspaces_test.go:
//
//	SERVICE-ONLY   Mounted on the same gwExempt /v1/service/ path as /service/members and gated by
//	               the same MemberSyncSecret. A tenant credential cannot reach this: tenant traffic
//	               does not travel on this path at all.
//	FAIL-CLOSED    secret == "" ⇒ every request 401s. A deployment that has not configured
//	               member-sync never serves the list of every workspace it hosts. Note this is NOT
//	               free: without the explicit guard, subtle.ConstantTimeCompare("", "") returns 1
//	               and an unconfigured deployment would admit a caller sending no token at all.
//	IDS ONLY       No names, no counts, no owners. A leaked response is a list of opaque slugs
//	               rather than a map of the business.
//
// INVALIDATED IF: this route stops being gwExempt or becomes user-facing; the secret gains a
// non-empty default; or the response grows a field beyond the id.

// workspaceLister is the slice of the store this handler needs. Narrow on purpose: the handler
// cannot reach anything else, so it cannot grow a richer response by accident.
type workspaceLister interface {
	ListWorkspaceIDs(ctx context.Context) ([]string, error)
}

// WorkspacesHandler serves GET /v1/service/workspaces.
type WorkspacesHandler struct {
	lister workspaceLister
	secret string
}

func NewWorkspacesHandler(store *Store, secret string) *WorkspacesHandler {
	return &WorkspacesHandler{lister: store, secret: secret}
}

func (h *WorkspacesHandler) Mount(r chi.Router) {
	r.Get("/service/workspaces", h.List)
}

type workspacesResponse struct {
	// Deliberately the only field. See the IDS ONLY note above.
	WorkspaceIDs []string `json:"workspace_ids"`
}

func (h *WorkspacesHandler) List(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or missing service token")
		return
	}
	ids, err := h.lister.ListWorkspaceIDs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "could not enumerate workspaces")
		return
	}
	if ids == nil {
		ids = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(workspacesResponse{WorkspaceIDs: ids})
}

// authorized is the ONLY gate on this endpoint, so it is written to fail closed twice over: an
// unset secret refuses everything before any comparison happens, and the comparison itself is
// constant-time so a token cannot be discovered a byte at a time.
func (h *WorkspacesHandler) authorized(r *http.Request) bool {
	if h.secret == "" {
		return false
	}
	raw := r.Header.Get("Authorization")
	if !strings.HasPrefix(raw, bearerPrefix) {
		return false
	}
	tok := strings.TrimPrefix(raw, bearerPrefix)
	return subtle.ConstantTimeCompare([]byte(tok), []byte(h.secret)) == 1
}
