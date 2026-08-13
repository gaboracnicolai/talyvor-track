package analytics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
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
//
// ⚠ THE REPORT IS RENDERED BEFORE ANY HEADER IS COMMITTED, AND THAT ORDERING IS THE WHOLE OF THIS
// FUNCTION'S CORRECTNESS. It used to write the status line and the download headers FIRST and then
// discard every error the engine returned (`_ = h.engine.Export…CSV(...)`), so a failed export was
// served as a successful download of nothing. MEASURED at 6e2906f, through this handler:
//
//	report=distribution&group_by=bogus  -> 200, attachment; filename="distribution-….csv", 0 bytes
//	report=velocity (no team_id)        -> 200, attachment; …,                 header row only
//	report=nosuchreport                 -> 200, attachment; filename="nosuchreport-….csv"
//
// while the JSON siblings refuse the first with 400 DISTRIBUTION_FAILED and the second with 400
// MISSING_TEAM. Held by export_refusal_test.go, which pins BOTH directions.
//
// ⚠ THE BUFFER IS NOT A NEW COST. All three Export…CSV methods call their Get… method first and
// materialise the whole report in memory before writing a byte, so the rendered CSV is bounded by
// data the engine already holds (velocity ≤ 50 cycles, ai-costs ≤ maxWindowDays rows, distribution
// one row per bucket). Nothing streams today, so nothing stops streaming.
//
// ⚠ THE STATUS AND CODE OF EACH FAILURE ARE THE SIBLING ROUTE'S OWN, COPIED RATHER THAN CHOSEN —
// distribution 400 DISTRIBUTION_FAILED, velocity 500 VELOCITY_FAILED, ai-costs 500 AICOSTS_FAILED.
// A caller that already handles the JSON route's errors handles these unchanged, and this function
// invents no policy about which failures are the caller's fault.
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

	var (
		buf    bytes.Buffer
		err    error
		status = http.StatusInternalServerError
		code   string
	)
	switch report {
	case "velocity":
		// team_id is REQUIRED here for the reason the JSON route already gives: GetVelocity reads
		// `WHERE c.team_id = $1`, and an empty team matches no cycle, so the old export answered a
		// request that named no team with a header-only CSV — indistinguishable from a team that
		// genuinely has no cycles.
		teamID := r.URL.Query().Get("team_id")
		if teamID == "" {
			writeErr(w, http.StatusBadRequest, "MISSING_TEAM", "team_id query parameter required")
			return
		}
		code = "VELOCITY_FAILED"
		err = h.engine.ExportVelocityCSV(r.Context(), teamID, wsID, intParam(r, "cycles", 5), &buf)
	case "ai-costs":
		code = "AICOSTS_FAILED"
		err = h.engine.ExportAICostTrendsCSV(r.Context(), wsID, intParam(r, "days", 30), &buf)
	case "distribution":
		gb := r.URL.Query().Get("group_by")
		if gb == "" {
			gb = "status"
		}
		// 400 rather than 500, matching Handler.Distribution: the one error this call returns
		// without touching the database is "unsupported group_by", which is the caller's input.
		status, code = http.StatusBadRequest, "DISTRIBUTION_FAILED"
		err = h.engine.ExportDistributionCSV(r.Context(), wsID, gb, intParam(r, "days", 30), &buf)
	default:
		writeErr(w, http.StatusBadRequest, "UNKNOWN_REPORT", "unknown report: "+report)
		return
	}
	if err != nil {
		writeErr(w, status, code, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-%s.csv"`, report, time.Now().UTC().Format("2006-01-02")))
	w.WriteHeader(http.StatusOK)
	// Past this point the response is committed and a write failure means the client went away;
	// there is no status left to change, so it is logged rather than swallowed silently.
	if _, werr := w.Write(buf.Bytes()); werr != nil {
		slog.Warn("analytics: export write failed",
			slog.String("report", report), slog.String("err", werr.Error()))
	}
}
