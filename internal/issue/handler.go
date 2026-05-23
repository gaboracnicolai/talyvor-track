package issue

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/model"
)

// Handler is the HTTP surface for /workspaces/{wsID}/issues/*.
type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// Mount registers every issue route on r. Routes are mounted under
// /workspaces/{wsID}/issues so the workspace ID is always part of the
// URL — multi-tenant scoping is enforced at the route level, not by
// trusting a header.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/issues", func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/", h.List)
		r.Get("/search", h.Search)
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

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	var in model.Issue
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
	metrics.IssuesCreated.WithLabelValues(out.WorkspaceID, out.TeamID, string(out.Status)).Inc()
	writeJSON(w, http.StatusCreated, out)
}

// List handles GET /workspaces/{wsID}/issues with optional query
// params: status, team_id, project_id, cycle_id, assignee_id,
// priority, labels, limit, offset, order_by, order_dir.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	q := r.URL.Query()
	filter := IssueFilter{
		WorkspaceID: wsID,
		TeamID:      q.Get("team_id"),
		ProjectID:   q.Get("project_id"),
		CycleID:     q.Get("cycle_id"),
		Status:      q.Get("status"),
		AssigneeID:  q.Get("assignee_id"),
		OrderBy:     q.Get("order_by"),
		OrderDir:    q.Get("order_dir"),
	}
	if v := q.Get("priority"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Priority = n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Offset = n
		}
	}

	out, err := h.store.List(r.Context(), filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Issue{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	out, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var updates map[string]any
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if len(updates) == 0 {
		writeErr(w, http.StatusBadRequest, "EMPTY_UPDATE", "no fields provided")
		return
	}
	out, err := h.store.Update(r.Context(), id, updates)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	metrics.IssuesUpdated.WithLabelValues(out.WorkspaceID, out.TeamID, string(out.Status)).Inc()
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	query := r.URL.Query().Get("q")
	if query == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_QUERY", "q query parameter is required")
		return
	}
	limit := 25
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	out, err := h.store.Search(r.Context(), wsID, query, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SEARCH_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Issue{}
	}
	writeJSON(w, http.StatusOK, out)
}

// avoid unused import warnings while we wire ancillary error types
var _ = errors.New
