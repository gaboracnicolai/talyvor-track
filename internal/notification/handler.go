package notification

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/notifications", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/read-all", h.MarkAllRead)
		r.Post("/{id}/read", h.MarkRead)
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

// SEC-5 (identity): notifications belong to the verified session member. The actor is always
// authz.MemberID(ctx) — a supplied member_id (query or body) is retired as an identity source,
// so no caller can read or mutate another member's notifications.

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	memberID, ok := authz.MemberID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no authorized member")
		return
	}
	unreadOnly := r.URL.Query().Get("unread_only") == "true"
	out, err := h.store.List(r.Context(), memberID, unreadOnly, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []Notification{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	memberID, ok := authz.MemberID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no authorized member")
		return
	}
	if err := h.store.MarkAllRead(r.Context(), memberID); err != nil {
		writeErr(w, http.StatusInternalServerError, "MARK_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	memberID, ok := authz.MemberID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no authorized member")
		return
	}
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if err := h.store.MarkRead(r.Context(), chi.URLParam(r, "id"), memberID, wsID); err != nil {
		writeErr(w, http.StatusInternalServerError, "MARK_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
