package dependency

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// Mount nests the relations endpoints under /workspaces/{wsID}/issues/{id}
// so the routes inherit the workspace + issue scoping naturally.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/issues/{id}/relations", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Post("/bulk", h.BulkCreate)
		r.Delete("/", h.Delete)
	})
	r.Get("/workspaces/{wsID}/issues/{id}/dependency-graph", h.Graph)

	// Workspace-level aggregations powering the sprint planner +
	// the relations dashboard. Live alongside the per-issue routes
	// so the URL hierarchy stays grouped under "relations".
	r.Get("/workspaces/{wsID}/relations/stats", h.Stats)
	r.Get("/workspaces/{wsID}/relations/blocking", h.Blocking)
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
	issueID := chi.URLParam(r, "id")
	rels, err := h.store.GetRelations(r.Context(), issueID, wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if rels == nil {
		rels = []RelationWithIssue{}
	}
	writeJSON(w, http.StatusOK, rels)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "FORBIDDEN"})
		return
	}
	actorID, ok := authz.MemberID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "FORBIDDEN"})
		return
	}
	sourceID := chi.URLParam(r, "id")
	var in struct {
		TargetID string       `json:"target_id"`
		Type     RelationType `json:"type"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	out, err := h.store.Create(r.Context(), Relation{
		SourceID: sourceID, TargetID: in.TargetID, Type: in.Type,
		WorkspaceID: wsID,
		CreatedBy:   actorID,
	})
	if err != nil {
		if errors.Is(err, ErrCycle) {
			writeErr(w, http.StatusConflict, "DEPENDENCY_CYCLE", err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "id")
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	var in struct {
		TargetID string       `json:"target_id"`
		Type     RelationType `json:"type"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if err := h.store.Delete(r.Context(), wsID, sourceID, in.TargetID, in.Type); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) Graph(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "FORBIDDEN"})
		return
	}
	issueID := chi.URLParam(r, "id")
	depth := 3
	if v := r.URL.Query().Get("depth"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			depth = n
		}
	}
	graph, err := h.store.GetDependencyGraph(r.Context(), wsID, issueID, depth)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "GRAPH_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

// ─── new endpoints ─────────────────────────────────────────

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "FORBIDDEN"})
		return
	}
	out, err := h.store.GetRelationStats(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "STATS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Blocking(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "FORBIDDEN"})
		return
	}
	var cycleID *string
	if v := r.URL.Query().Get("cycle_id"); v != "" {
		cycleID = &v
	}
	out, err := h.store.GetBlockingIssues(r.Context(), wsID, cycleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "BLOCKING_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []BlockingIssue{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) BulkCreate(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "FORBIDDEN"})
		return
	}
	actorID, ok := authz.MemberID(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, apiError{Error: "not a member of this workspace", Code: "FORBIDDEN"})
		return
	}
	sourceID := chi.URLParam(r, "id")
	var in struct {
		TargetIDs []string     `json:"target_ids"`
		Type      RelationType `json:"type"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	count, err := h.store.BulkCreateRelations(r.Context(),
		Relation{
			SourceID: sourceID, WorkspaceID: wsID, Type: in.Type,
			CreatedBy: actorID,
		},
		in.TargetIDs,
	)
	if err != nil {
		if errors.Is(err, ErrCycle) {
			writeErr(w, http.StatusConflict, "DEPENDENCY_CYCLE", err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, "BULK_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int{"created": count})
}
