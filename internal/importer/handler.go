package importer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
)

// Handler exposes the importer over HTTP. It owns no state — every
// import is processed inline against the Importer's issue store.
//
// ⚠ WHAT BOUNDS AN UPLOAD, MEASURED RATHER THAN ASSERTED. This comment used to read "the
// multipart upload is capped at maxUploadBytes; anything larger is rejected before we start
// parsing", and every clause of that was wrong:
//
//   - maxUploadBytes is ParseMultipartForm's maxMemory argument. It rejects NOTHING — it is
//     the point at which file parts stop being held in RAM and start spilling to temp files
//     on disk.
//   - The size that does reject is httpx.ImportMaxBody (96 MiB), applied as router
//     middleware in cmd/track/main.go. A different number, in a different package.
//   - It is not rejected "before we start parsing" but DURING: the read fails partway
//     through the body. MEASURED on the production stack — a 120 MiB upload is refused
//     after 100,663,297 bytes (96.0 MiB) have crossed the wire.
//   - The refusal surfaces as 400 BAD_UPLOAD carrying net/http's "request body too large",
//     not the 413 BODY_TOO_LARGE every other route answers (httpx.DecodeJSON maps
//     *http.MaxBytesError explicitly). Reported in the queue rather than changed here: it is
//     a shipped status code on a public route.
type Handler struct{ imp *Importer }

func NewHandler(imp *Importer) *Handler { return &Handler{imp: imp} }

// maxUploadBytes is how much of a multipart upload is buffered in MEMORY before the
// remainder spills to a temp file — not a size limit. See the Handler doc above.
const maxUploadBytes = 64 << 20 // 64 MiB

func (h *Handler) Mount(r chi.Router) {
	r.Post("/import/linear", h.linear)
	r.Post("/import/jira", h.jira)
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

func (h *Handler) linear(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, h.imp.ImportLinearCSV)
}

func (h *Handler) jira(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, h.imp.ImportJiraCSV)
}

// importFn is the shape both ImportLinearCSV and ImportJiraCSV satisfy.
// Aliasing the signature keeps the dispatch one line.
type importFn func(ctx context.Context, workspaceID, teamID string, r io.Reader) (*ImportResult, error)

func (h *Handler) run(w http.ResponseWriter, r *http.Request, fn importFn) {
	// WHO MAY IMPORT is decided before WHAT IS BEING IMPORTED is read. Neither check below
	// touches the body: workspace_id/team_id come from the URL query (r.URL.Query(), not
	// r.FormValue, so no parse dependency) and the memberships were resolved into the
	// request context by the T10 middleware.
	//
	// ⚠ THE PARSE USED TO RUN FIRST AND THAT COST A 403'd CALLER'S WHOLE UPLOAD. MEASURED on
	// 666ce7a against real Postgres on the production middleware stack: a caller with a valid
	// gateway identity and NO membership in the target workspace POSTed 40 MiB and the server
	// read all 41,943,379 bytes — buffering up to maxUploadBytes of it in heap and spilling
	// the rest to a temp file — before answering "not a member of this workspace". Every byte
	// was spent on a request that was never authorized, and the type comment above claimed
	// that could not happen. Held by TestImporter_NonMemberUploadIsNotRead, which asserts the
	// non-member's read is ZERO and requires a byte-identical member upload to be read in full
	// and land, so the zero cannot come from an empty fixture.
	workspaceID := r.URL.Query().Get("workspace_id")
	teamID := r.URL.Query().Get("team_id")
	if workspaceID == "" || teamID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id and team_id are required (query string)")
		return
	}
	// This is a flat /v1/import/* route (no path {wsID}), so T10 resolved the caller's
	// memberships but authorized no single workspace. Authorize the caller-supplied
	// workspace_id against those memberships — not a member → 403. The workspace then comes
	// from the membership row (server-resolved), never trusted from the query alone.
	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_UPLOAD", err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "NO_FILE", "expected multipart 'file' field")
		return
	}
	defer file.Close()

	out, err := fn(r.Context(), m.WorkspaceID, teamID, file)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "IMPORT_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
