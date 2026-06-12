package notification

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// deadLetterLister is the read side of the dead-letter surface. *DeadLetterStore
// satisfies it; the handler keeps it as an interface so the route can be tested
// without a database.
type deadLetterLister interface {
	List(ctx context.Context, limit int) ([]DeadLetter, error)
}

type Handler struct {
	store       *Store
	deadLetters deadLetterLister
}

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// WithDeadLetters wires the read-only admin surface for failed email
// deliveries. Optional: without it, the route returns an empty list.
func (h *Handler) WithDeadLetters(l deadLetterLister) *Handler {
	h.deadLetters = l
	return h
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/notifications", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/read-all", h.MarkAllRead)
		r.Post("/{id}/read", h.MarkRead)
		// Admin: messages the email queue permanently failed to deliver.
		r.Get("/dead-letters", h.ListDeadLetters)
	})
}

// ListDeadLetters returns the most recent permanently-failed email deliveries.
// Returns an empty array when email/dead-letter is not configured (nothing
// sends, so nothing can fail).
func (h *Handler) ListDeadLetters(w http.ResponseWriter, r *http.Request) {
	out := []DeadLetter{}
	if h.deadLetters != nil {
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		rows, err := h.deadLetters.List(r.Context(), limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "DLQ_LIST_FAILED", err.Error())
			return
		}
		if rows != nil {
			out = rows
		}
	}
	writeJSON(w, http.StatusOK, out)
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
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
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
