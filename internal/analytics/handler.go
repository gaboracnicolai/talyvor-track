package analytics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
)

type Handler struct{ engine *Engine }

func NewHandler(engine *Engine) *Handler { return &Handler{engine: engine} }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/analytics", func(r chi.Router) {
		r.Get("/velocity", h.Velocity)
		r.Get("/burndown", h.Burndown)
		r.Get("/distribution", h.Distribution)
		r.Get("/resolution", h.Resolution)
		r.Get("/ai-costs", h.AICosts)
		r.Get("/workload", h.Workload)
		r.Get("/export", h.Export)
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

func intParam(r *http.Request, name string, fallback int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return fallback
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return fallback
}

func (h *Handler) Velocity(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	teamID := r.URL.Query().Get("team_id")
	if teamID == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_TEAM", "team_id query parameter required")
		return
	}
	out, err := h.engine.GetVelocity(r.Context(), teamID, wsID, intParam(r, "cycles", 5))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "VELOCITY_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []CycleVelocity{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Burndown(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	cycleID := r.URL.Query().Get("cycle_id")
	if cycleID == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_CYCLE", "cycle_id query parameter required")
		return
	}
	rep, err := h.engine.GetBurndown(r.Context(), cycleID, wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "BURNDOWN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) Distribution(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "WORKSPACE_FORBIDDEN", "not a member of this workspace")
		return
	}
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "status"
	}
	out, err := h.engine.GetDistribution(r.Context(), wsID, groupBy, intParam(r, "days", 30))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "DISTRIBUTION_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []DistributionBucket{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Resolution(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "WORKSPACE_FORBIDDEN", "not a member of this workspace")
		return
	}
	out, err := h.engine.GetTimeToResolution(r.Context(),
		wsID, r.URL.Query().Get("team_id"),
		intParam(r, "days", 30))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "RESOLUTION_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) AICosts(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "WORKSPACE_FORBIDDEN", "not a member of this workspace")
		return
	}
	out, err := h.engine.GetAICostTrends(r.Context(), wsID, intParam(r, "days", 30))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "AICOSTS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Workload(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "WORKSPACE_FORBIDDEN", "not a member of this workspace")
		return
	}
	out, err := h.engine.GetWorkload(r.Context(), wsID, r.URL.Query().Get("team_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "WORKLOAD_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []MemberWorkload{}
	}
	writeJSON(w, http.StatusOK, out)
}

// Export streams a CSV file for the requested report. The Content-
// Disposition header pins the filename so browsers offer a download
// rather than inline display.
func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "WORKSPACE_FORBIDDEN", "not a member of this workspace")
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" {
		writeErr(w, http.StatusBadRequest, "UNSUPPORTED_FORMAT", "only csv is supported")
		return
	}
	report := r.URL.Query().Get("report")
	if report == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_REPORT", "report query parameter required")
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-%s.csv"`, report, time.Now().UTC().Format("2006-01-02")))
	w.WriteHeader(http.StatusOK)

	switch report {
	case "velocity":
		teamID := r.URL.Query().Get("team_id")
		_ = h.engine.ExportVelocityCSV(r.Context(), teamID, wsID, intParam(r, "cycles", 5), w)
	case "ai-costs":
		_ = h.engine.ExportAICostTrendsCSV(r.Context(), wsID, intParam(r, "days", 30), w)
	case "distribution":
		gb := r.URL.Query().Get("group_by")
		if gb == "" {
			gb = "status"
		}
		_ = h.engine.ExportDistributionCSV(r.Context(), wsID, gb, intParam(r, "days", 30), w)
	default:
		// Headers are already committed; we can't switch to a JSON
		// error response. Stream a single-line CSV explaining the
		// problem so the caller's spreadsheet shows the message.
		_, _ = w.Write([]byte("error\nunknown report: " + report + "\n"))
	}
}
