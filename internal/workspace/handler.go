package workspace

import (
	"encoding/json"
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
	r.Route("/workspaces", func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/", h.List)
		r.Get("/{wsID}", h.Get)
		r.Patch("/{wsID}", h.Update)
		r.Delete("/{wsID}", h.Delete)
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

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if err := h.store.Delete(r.Context(), wsID); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
