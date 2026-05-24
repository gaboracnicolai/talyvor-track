package timetracking

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// Mount registers the time-tracking endpoints. Timer endpoints sit
// at /timer/* so they're easy to grep when debugging "why isn't my
// timer running"; the per-issue list lives under the issue resource
// to mirror Linear/Jira URL shape.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}", func(r chi.Router) {
		r.Get("/timer", h.GetTimer)
		r.Post("/timer/start", h.StartTimer)
		r.Post("/timer/stop", h.StopTimer)

		r.Post("/time-entries", h.LogTime)
		r.Delete("/time-entries/{id}", h.Delete)

		r.Get("/issues/{id}/time-entries", h.ListIssueEntries)
		r.Get("/time-summary", h.WorkspaceSummary)
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

func (h *Handler) GetTimer(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	memberID := r.URL.Query().Get("member_id")
	if memberID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "member_id required")
		return
	}
	out, err := h.store.GetRunningTimer(r.Context(), memberID, wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "TIMER_READ_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) StartTimer(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	var in struct {
		IssueID     string `json:"issue_id"`
		MemberID    string `json:"member_id"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	out, err := h.store.StartTimer(r.Context(), in.IssueID, wsID, in.MemberID, in.Description)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "START_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) StopTimer(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	var in struct {
		MemberID string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	out, err := h.store.StopTimer(r.Context(), in.MemberID, wsID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "STOP_FAILED", err.Error())
		return
	}
	if out == nil {
		// No running entry — return 200 with a sentinel rather than
		// 404 so the frontend doesn't have to special-case the path.
		writeJSON(w, http.StatusOK, map[string]bool{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// logTimeRequest mirrors TimeEntry but uses a pointer for Billable so
// "omitted" is distinguishable from "explicit false". We default to
// true when omitted, matching the column default — but respect an
// explicit `"billable": false` from non-billable internal work.
type logTimeRequest struct {
	IssueID     string     `json:"issue_id"`
	MemberID    string     `json:"member_id"`
	Description string     `json:"description"`
	StartedAt   time.Time  `json:"started_at"`
	StoppedAt   *time.Time `json:"stopped_at"`
	Billable    *bool      `json:"billable"`
}

func (h *Handler) LogTime(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	var in logTimeRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	billable := true
	if in.Billable != nil {
		billable = *in.Billable
	}
	entry := TimeEntry{
		IssueID:     in.IssueID,
		WorkspaceID: wsID,
		MemberID:    in.MemberID,
		Description: in.Description,
		StartedAt:   in.StartedAt,
		StoppedAt:   in.StoppedAt,
		Billable:    billable,
	}
	out, err := h.store.LogTime(r.Context(), entry)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "LOG_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) ListIssueEntries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	entries, err := h.store.ListByIssue(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if entries == nil {
		entries = []TimeEntry{}
	}
	summary, err := h.store.GetIssueSummary(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SUMMARY_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"summary": summary,
	})
}

func (h *Handler) WorkspaceSummary(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "wsID")
	since := time.Now().UTC().AddDate(0, 0, -30) // default last 30 days
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}
	out, err := h.store.GetWorkspaceSummary(r.Context(), wsID, since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SUMMARY_FAILED", err.Error())
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
