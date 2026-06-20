package notification

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/httpx"
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

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	memberID := r.URL.Query().Get("member_id")
	if memberID == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_MEMBER", "member_id query parameter required")
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
	var in struct {
		MemberID string `json:"member_id"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if err := h.store.MarkAllRead(r.Context(), in.MemberID); err != nil {
		writeErr(w, http.StatusInternalServerError, "MARK_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	if err := h.store.MarkRead(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "MARK_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
