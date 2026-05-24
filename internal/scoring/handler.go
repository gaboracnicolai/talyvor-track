package scoring

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// Mount registers per-issue score endpoints alongside the prioritised
// backlog + summary endpoints. The score routes nest under the issue
// path so the JSON for an issue's "score" lives at the issue URL —
// matches the rest of Track's REST surface.
func (h *Handler) Mount(r chi.Router) {
	r.Put("/workspaces/{wsID}/issues/{id}/score", h.Set)
	r.Get("/workspaces/{wsID}/issues/{id}/score", h.Get)
	r.Delete("/workspaces/{wsID}/issues/{id}/score", h.Delete)

	r.Get("/workspaces/{wsID}/backlog/prioritized", h.PrioritizedBacklog)
	r.Get("/workspaces/{wsID}/scoring/summary", h.Summary)
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

func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	id := chi.URLParam(r, "id")
	var in struct {
		Method ScoringMethod `json:"method"`
		RICE   *RICEScore    `json:"rice"`
		ICE    *ICEScore     `json:"ice"`
		Notes  string        `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	out, err := h.store.SetScore(r.Context(), id, wsID,
		r.Header.Get("X-Member-Id"), in.Method, in.RICE, in.ICE, in.Notes)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "SET_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.GetScore(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "no score for this issue")
			return
		}
		writeErr(w, http.StatusInternalServerError, "GET_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteScore(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) PrioritizedBacklog(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	method := ScoringMethod(r.URL.Query().Get("method"))
	if method == "" {
		method = ScoringRICE
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	var teamID *string
	if v := r.URL.Query().Get("team_id"); v != "" {
		teamID = &v
	}

	out, err := h.store.GetPrioritizedBacklog(r.Context(), wsID, teamID, method, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "BACKLOG_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []ScoredIssue{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	out, err := h.store.GetScoreSummary(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SUMMARY_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
