package ai

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
)

type Handler struct {
	engine *Engine
	issues *issue.Store
}

func NewHandler(engine *Engine, issues *issue.Store) *Handler {
	return &Handler{engine: engine, issues: issues}
}

func (h *Handler) Mount(r chi.Router) {
	// Issue-level AI actions.
	r.Post("/workspaces/{wsID}/issues/{id}/triage", h.Triage)
	r.Post("/workspaces/{wsID}/issues/{id}/find-duplicates", h.FindDuplicates)
	r.Get("/workspaces/{wsID}/issues/{id}/summary", h.Summary)

	// Sprint planning.
	r.Post("/workspaces/{wsID}/teams/{teamID}/cycles/{id}/suggest-issues", h.SuggestSprint)

	// Semantic search at the workspace level.
	r.Get("/workspaces/{wsID}/issues/semantic-search", h.SemanticSearch)
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

// unavailable is the "Lens not configured" response. 200 OK with a
// flag — the frontend renders an empty-state card instead of an error.
func unavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]bool{"ai_available": false})
}

func (h *Handler) Triage(w http.ResponseWriter, r *http.Request) {
	if !h.engine.IsAvailable() {
		unavailable(w)
		return
	}
	iss, err := h.issues.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || iss == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "issue not found")
		return
	}
	result, err := h.engine.TriageIssue(r.Context(), *iss)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "AI_ERROR", err.Error())
		return
	}

	// Optional auto-apply: ?apply=true overwrites priority + labels
	// based on the LLM's suggestion. The user can revert any time.
	if r.URL.Query().Get("apply") == "true" {
		updates := map[string]any{
			"priority": int(result.SuggestedPriority),
			"labels":   result.SuggestedLabels,
		}
		_, _ = h.issues.Update(r.Context(), iss.ID, updates)
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) FindDuplicates(w http.ResponseWriter, r *http.Request) {
	if !h.engine.IsAvailable() {
		unavailable(w)
		return
	}
	iss, err := h.issues.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || iss == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "issue not found")
		return
	}
	// Pull a recency-ordered candidate window so we don't ship the
	// entire issue tree to the LLM. 20 is plenty for triage.
	candidates, _ := h.issues.List(r.Context(), issue.IssueFilter{
		WorkspaceID: chi.URLParam(r, "wsID"),
		TeamID:      iss.TeamID,
		Limit:       20,
		OrderBy:     "created_at",
		OrderDir:    "desc",
	})
	out, err := h.engine.FindDuplicates(r.Context(), *iss, candidates)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "AI_ERROR", err.Error())
		return
	}
	if out == nil {
		out = []DuplicateCandidate{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	iss, err := h.issues.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || iss == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "issue not found")
		return
	}
	comments, _ := h.issues.ListComments(r.Context(), iss.ID)
	out, err := h.engine.SummarizeThread(r.Context(), *iss, comments)
	if err != nil {
		if err == ErrAIUnavailable {
			unavailable(w)
			return
		}
		writeErr(w, http.StatusBadGateway, "AI_ERROR", err.Error())
		return
	}
	if out == nil {
		writeJSON(w, http.StatusOK, map[string]any{"summary_available": false, "min_comments": summaryMinComments})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) SuggestSprint(w http.ResponseWriter, r *http.Request) {
	if !h.engine.IsAvailable() {
		unavailable(w)
		return
	}
	var in struct {
		TeamSize  int `json:"team_size"`
		CycleDays int `json:"cycle_days"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if in.TeamSize <= 0 {
		in.TeamSize = 5
	}
	if in.CycleDays <= 0 {
		in.CycleDays = 14
	}
	backlog, _ := h.issues.List(r.Context(), issue.IssueFilter{
		WorkspaceID: chi.URLParam(r, "wsID"),
		TeamID:      chi.URLParam(r, "teamID"),
		Status:      "backlog",
		Limit:       100,
		OrderBy:     "priority",
		OrderDir:    "asc",
	})
	out, err := h.engine.SuggestSprintIssues(r.Context(), chi.URLParam(r, "teamID"), backlog, in.CycleDays, in.TeamSize)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "AI_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) SemanticSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_QUERY", "q query parameter required")
		return
	}
	limit := 25
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	out, err := h.engine.SemanticSearch(r.Context(), chi.URLParam(r, "wsID"), query, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SEARCH_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Issue{}
	}
	writeJSON(w, http.StatusOK, out)
}
