package importer

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/tenancy"
)

// job_handler.go — T8 Build B: the ASYNC import surface. A SEPARATE handler from the synchronous Handler
// (handler.go), so the sync endpoints + their tenancy regression lock (authz_test.go) stay byte-identical.
// It reuses the same authz.AuthorizeWorkspace gate and the same writeErr/writeJSON helpers.

// integrationChecker is the tenancy-scoped existence check for a workspace's provider integration (C.1's
// store satisfies it). A local interface — job_handler does NOT import integrations; main.go injects it.
type integrationChecker interface {
	Configured(ctx context.Context, workspaceID, provider string) (bool, error)
}

type JobHandler struct {
	jobs         *JobStore
	integrations integrationChecker // nil ⇒ live API (*_api) import disabled
}

func NewJobHandler(jobs *JobStore) *JobHandler { return &JobHandler{jobs: jobs} }

// WithIntegrationChecker enables *_api enqueue by wiring the credential store's existence check. Absent ⇒
// an *_api enqueue returns a clean 409 (live API import unavailable), never a panic.
func (h *JobHandler) WithIntegrationChecker(c integrationChecker) *JobHandler {
	h.integrations = c
	return h
}

const jobMaxUploadBytes = 64 << 20 // 64 MiB — mirrors the sync handler cap

// validSourceTypes is the CSV (file-upload) set. validAPISourceTypes is the payload-less live-import set.
var validSourceTypes = map[string]bool{"linear_csv": true, "jira_csv": true}
var validAPISourceTypes = map[string]bool{"linear_api": true, "jira_api": true}

func providerFromAPISource(sourceType string) string {
	switch sourceType {
	case "linear_api":
		return "linear"
	case "jira_api":
		return "jira"
	default:
		return ""
	}
}

func (h *JobHandler) Mount(r chi.Router) {
	r.Post("/import/jobs", h.create)
	r.Get("/import/jobs/{id}", h.status)
}

// create authorizes the target workspace (the SAME gate as the sync path), persists the job + its payload
// atomically, and returns 202 + job_id. The workspace written to the job row is the SERVER-RESOLVED
// m.WorkspaceID — never the raw query param.
func (h *JobHandler) create(w http.ResponseWriter, r *http.Request) {
	// T8 C.4 enqueue: an *_api job carries NO file/payload — its source config lives in the C.1 integration
	// store. Intercept it before the multipart path; every other source_type (the *_csv pair + any invalid
	// value) falls through to the UNCHANGED upload path below.
	if validAPISourceTypes[r.URL.Query().Get("source_type")] {
		h.createAPI(w, r)
		return
	}
	if err := r.ParseMultipartForm(jobMaxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_UPLOAD", err.Error())
		return
	}
	workspaceID := r.URL.Query().Get("workspace_id")
	teamID := r.URL.Query().Get("team_id")
	sourceType := r.URL.Query().Get("source_type")
	if workspaceID == "" || teamID == "" || sourceType == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id, team_id, source_type are required (query string)")
		return
	}
	if !validSourceTypes[sourceType] {
		writeErr(w, http.StatusBadRequest, "BAD_SOURCE_TYPE", "source_type must be linear_csv or jira_csv")
		return
	}
	// SAME tenancy gate as the sync path: authorize the caller-supplied workspace against memberships; the
	// workspace persisted below is the server-resolved membership row (m.WorkspaceID), never the query alone.
	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "NO_FILE", "expected multipart 'file' field")
		return
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, jobMaxUploadBytes+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}
	if len(payload) > jobMaxUploadBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "TOO_LARGE", "payload exceeds 64MiB")
		return
	}

	jobID, err := h.jobs.Create(r.Context(), m.WorkspaceID, teamID, sourceType, payload)
	if err != nil {
		// A cross-workspace team_id is a client error (fail-fast, before any row is written), not a 500.
		if errors.Is(err, tenancy.ErrCrossWorkspace) {
			writeErr(w, http.StatusBadRequest, "CROSS_WORKSPACE_TEAM", "team_id is not in the authorized workspace")
			return
		}
		writeErr(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": JobPending})
}

// createAPI enqueues a payload-less *_api job (Linear/Jira live import). No file: the source config lives in
// the C.1 integration store. Same authz gate + same team cross-object guard as the *_csv path; adds a
// fail-fast check that the authorized workspace actually HAS the provider integration (tenancy-scoped) so a
// doomed job is never created.
func (h *JobHandler) createAPI(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	teamID := r.URL.Query().Get("team_id")
	sourceType := r.URL.Query().Get("source_type")
	if workspaceID == "" || teamID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id and team_id are required (query string)")
		return
	}
	// SAME tenancy gate as the *_csv path (and the sync path). A caller not a member of workspaceID is 403 —
	// they cannot enqueue for another workspace, and the check below runs only against m.WorkspaceID.
	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	// Integrations disabled (no encryption key) ⇒ live API import unavailable — clean 409, mirrors the runner.
	if h.integrations == nil {
		writeErr(w, http.StatusConflict, "API_IMPORT_DISABLED", "live API import unavailable (integrations not configured)")
		return
	}
	provider := providerFromAPISource(sourceType)
	// FAIL-FAST: the authorized workspace must have the provider integration. Tenancy-scoped to m.WorkspaceID
	// — never reads another workspace's integrations. Absent ⇒ 400, and NO job row is created.
	configured, err := h.integrations.Configured(r.Context(), m.WorkspaceID, provider)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LOOKUP_FAILED", err.Error())
		return
	}
	if !configured {
		writeErr(w, http.StatusBadRequest, "NO_INTEGRATION", "no "+provider+" integration configured for this workspace")
		return
	}
	// Payload-less job; same team cross-object guard as *_csv (a team from another workspace ⇒ 400, no row).
	jobID, err := h.jobs.CreateAPIJob(r.Context(), m.WorkspaceID, teamID, sourceType)
	if err != nil {
		if errors.Is(err, tenancy.ErrCrossWorkspace) {
			writeErr(w, http.StatusBadRequest, "CROSS_WORKSPACE_TEAM", "team_id is not in the authorized workspace")
			return
		}
		writeErr(w, http.StatusInternalServerError, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": JobPending})
}

// status loads the job, then authorizes the CALLER against the JOB'S workspace — a caller who is not a member
// of that workspace is DENIED (403), never shown another tenant's job data. Unknown job ⇒ 404.
func (h *JobHandler) status(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, err := h.jobs.Get(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LOOKUP_FAILED", err.Error())
		return
	}
	if job == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "no such import job")
		return
	}
	// TENANCY: scope the read to the job's workspace. Not a member ⇒ 403 (never the data).
	if _, ok := authz.AuthorizeWorkspace(r.Context(), job.WorkspaceID); !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this job's workspace")
		return
	}
	writeJSON(w, http.StatusOK, job)
}
