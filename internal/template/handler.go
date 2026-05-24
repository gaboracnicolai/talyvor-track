package template

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/templates", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Get("/{id}", h.Get)
		r.Patch("/{id}", h.Update)
		r.Delete("/{id}", h.Delete)
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
	wsID := chi.URLParam(r, "wsID")
	var teamID *string
	if v := r.URL.Query().Get("team_id"); v != "" {
		teamID = &v
	}
	out, err := h.store.List(r.Context(), wsID, teamID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []IssueTemplate{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	var in IssueTemplate
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	in.WorkspaceID = wsID
	out, err := h.store.Create(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var updates map[string]any
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	out, err := h.store.Update(r.Context(), chi.URLParam(r, "id"), updates)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
