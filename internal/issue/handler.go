package issue

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/model"
)

// notifier is the subset of internal/realtime.Notifier the issue
// handler depends on. Defined locally as an interface so the issue
// package doesn't import realtime — the WS infrastructure stays
// optional and the import graph stays simple.
type notifier interface {
	IssueCreated(ctx context.Context, wsID, teamID, actorID string, issue model.Issue)
	IssueUpdated(ctx context.Context, wsID, teamID, issueID, actorID string, changes map[string]any)
	IssueDeleted(ctx context.Context, wsID, teamID, issueID, actorID string)
	CommentCreated(ctx context.Context, wsID, issueID, actorID string, comment model.Comment)
	CommentUpdated(ctx context.Context, wsID, issueID, actorID string, comment model.Comment)
	CommentDeleted(ctx context.Context, wsID, issueID, commentID, actorID string)
}

// Handler is the HTTP surface for /workspaces/{wsID}/issues/*.
type Handler struct {
	store    *Store
	notifier notifier
}

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// WithNotifier wires the realtime notifier so every successful issue
// or comment mutation fans out over WebSockets. Optional — without
// it, the handler is fully functional but no live updates fire.
func (h *Handler) WithNotifier(n notifier) *Handler {
	h.notifier = n
	return h
}

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

		// Comments live under the issue. POST takes the actor in the
		// X-Member-Id header — Phase 4's auth pass will replace that
		// with a real identity claim.
		r.Post("/{id}/comments", h.CreateComment)
		r.Get("/{id}/comments", h.ListComments)
		r.Patch("/{id}/comments/{commentID}", h.UpdateComment)
		r.Delete("/{id}/comments/{commentID}", h.DeleteComment)
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
	if h.notifier != nil {
		h.notifier.IssueCreated(r.Context(), out.WorkspaceID, out.TeamID, out.CreatorID, *out)
	}
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
	if h.notifier != nil {
		actorID := r.Header.Get("X-Member-Id")
		h.notifier.IssueUpdated(r.Context(), out.WorkspaceID, out.TeamID, out.ID, actorID, updates)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Look up the issue first so we know which team room to broadcast
	// to — once the soft-delete runs, the status is "cancelled" but
	// the team_id is still intact.
	existing, _ := h.store.GetByID(r.Context(), id)
	if err := h.store.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	if h.notifier != nil && existing != nil {
		actorID := r.Header.Get("X-Member-Id")
		h.notifier.IssueDeleted(r.Context(), existing.WorkspaceID, existing.TeamID, id, actorID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// CreateComment appends a comment to an issue and fans out a
// comment.created event to the issue's room. The author_id comes
// from the X-Member-Id header (Phase 4 will replace with auth).
func (h *Handler) CreateComment(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	var in model.Comment
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	in.IssueID = issueID
	if in.AuthorID == "" {
		in.AuthorID = r.Header.Get("X-Member-Id")
	}
	out, err := h.store.CreateComment(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	if h.notifier != nil {
		h.notifier.CommentCreated(r.Context(), chi.URLParam(r, "wsID"), issueID, out.AuthorID, *out)
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) ListComments(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListComments(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Comment{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) UpdateComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	out, err := h.store.UpdateComment(r.Context(), chi.URLParam(r, "commentID"), in.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	if h.notifier != nil {
		actorID := r.Header.Get("X-Member-Id")
		h.notifier.CommentUpdated(r.Context(), chi.URLParam(r, "wsID"), chi.URLParam(r, "id"), actorID, *out)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	commentID := chi.URLParam(r, "commentID")
	if err := h.store.DeleteComment(r.Context(), commentID); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	if h.notifier != nil {
		actorID := r.Header.Get("X-Member-Id")
		h.notifier.CommentDeleted(r.Context(), chi.URLParam(r, "wsID"), chi.URLParam(r, "id"), commentID, actorID)
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
