package issueboard

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// Mount wires two trees:
//   - /v1/workspaces/{wsID}/issue-boards — the workspace's links. Any member lists them; only an
//     owner turns one on or off, because a link publishes the workspace's issue titles.
//   - /v1/public/issue-boards/{token} — anonymous (gwExempt's /v1/public/ prefix), read-only.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/issue-boards", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Delete("/{id}", h.Revoke)
	})
	r.Get("/public/issue-boards/{token}", h.Public)
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
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	out, err := h.store.List(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if !authz.IsOwner(r.Context()) {
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "only a workspace owner can publish a board")
		return
	}
	memberID, ok := authz.MemberID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	// Decoded into Share, as project and cycle creates decode into their model: only ProjectID is
	// read from it — the workspace, author and token are this handler's, never the caller's.
	var in Share
	if r.ContentLength != 0 && !httpx.DecodeJSON(w, r, &in) {
		return
	}
	out, err := h.store.Create(r.Context(), wsID, in.ProjectID, memberID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if !authz.IsOwner(r.Context()) {
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "only a workspace owner can turn a board link off")
		return
	}
	if err := h.store.Revoke(r.Context(), chi.URLParam(r, "id"), wsID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "REVOKE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func validToken(t string) bool {
	if len(t) < 16 || len(t) > 64 {
		return false
	}
	for _, c := range t {
		if !tokenRune(c) {
			return false
		}
	}
	return true
}

// tokenRune reports whether c is in the URL-safe base64 alphabet a link token is written in.
func tokenRune(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		return true
	}
	return false
}

func (h *Handler) Public(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if !validToken(token) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "board not found")
		return
	}
	b, err := h.store.Public(r.Context(), token)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "board not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "BOARD_FAILED", "the board could not be read")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, b)
}
