package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/model"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (h *Handler) Mount(r chi.Router) {
	// Bootstrap sits OUTSIDE /workspaces on purpose: authz.workspaceIDFromPath reads the
	// third segment of /v1/workspaces/... as a {wsID}, so /v1/workspaces/bootstrap would be
	// checked against the caller's memberships and 403'd — and a caller with no workspace
	// has none by definition. It still runs behind the same gatewayauth + authz chain as
	// everything else here (Mount receives the /v1 router); it simply carries no {wsID} for
	// the membership check to bind to. See bootstrap.go for why it is a route at all.
	r.Post("/bootstrap", h.Bootstrap)

	r.Route("/workspaces", func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/", h.List)
		r.Get("/{wsID}", h.Get)
		r.Patch("/{wsID}", h.Update)
		r.Delete("/{wsID}", h.Delete)
		r.Post("/{wsID}/restore", h.Restore)
	})
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

// Create makes a workspace and seeds the verified caller as its owner (atomic). The
// route has no {wsID}, so the caller's verified email (T9) is the actor.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in model.Workspace
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	id, ok := gatewayauth.IdentityFrom(r.Context())
	if !ok || id.Email == "" {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no verified identity")
		return
	}
	out, err := h.store.CreateWithOwner(r.Context(), in, id.Email)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// List returns ONLY the caller's own workspaces (scoped to membership) — never an
// enumeration of all workspaces.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	ms, ok := authz.Memberships(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no verified identity")
		return
	}
	// ?deleted=true lists the workspaces the caller OWNS that are deleted and not yet purged — the
	// ones they can still restore — each with deleted_at and restorable_until.
	if r.URL.Query().Get("deleted") == "true" {
		owned := make([]string, 0, len(ms))
		for _, m := range ms {
			if authz.IsOwnerRole(m.Role) {
				owned = append(owned, m.WorkspaceID)
			}
		}
		out, err := h.store.ListDeletedByIDs(r.Context(), owned)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.WorkspaceID)
	}
	out, err := h.store.ListByIDs(r.Context(), ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Workspace{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	out, err := h.store.GetByID(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if !authz.IsOwner(r.Context()) { // owner-gated: changing workspace settings
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")
		return
	}
	var updates map[string]any
	if !httpx.DecodeJSON(w, r, &updates) {
		return
	}
	out, err := h.store.Update(r.Context(), wsID, updates)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Delete marks the workspace deleted: from then on every route answers 410 WORKSPACE_DELETED except
// restore, and the owner can restore it intact for 14 days (RestoreWindow); after that the purge sweep
// removes it and everything it owns. Owner only, and it must be confirmed: the body carries the
// workspace's slug, {"confirm": "<slug>"}, so a stray DELETE cannot remove a workspace.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if !authz.IsOwner(r.Context()) { // owner-gated: deleting the entire workspace
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")
		return
	}
	ws, err := h.store.GetByID(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&in) // an absent or malformed body is an unconfirmed delete
	}
	if in.Confirm != ws.Slug {
		writeErr(w, http.StatusBadRequest, "CONFIRMATION_REQUIRED",
			fmt.Sprintf(`deleting %q removes every issue, project and member in it after 14 days; to confirm, send {"confirm": %q}`, ws.Name, ws.Slug))
		return
	}
	out, err := h.store.Delete(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Restore brings a deleted workspace back exactly as it was, while its restore window is open. Owner
// only. authz lets this one route through for a deleted workspace.
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if !authz.IsOwner(r.Context()) {
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")
		return
	}
	out, err := h.store.Restore(r.Context(), wsID)
	if errors.Is(err, ErrNotRestorable) {
		writeErr(w, http.StatusConflict, "NOT_RESTORABLE", "this workspace is not deleted, or its 14 days to restore have passed")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "RESTORE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
