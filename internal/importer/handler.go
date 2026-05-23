package importer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Handler exposes the importer over HTTP. It owns no state — every
// import is processed inline against the Importer's issue store.
//
// The multipart upload is capped at maxUploadBytes; anything larger
// is rejected before we start parsing. The cap is generous enough
// for a real Linear/Jira export (tens of thousands of issues × ~1KB
// each) without letting a single request consume unbounded memory.
type Handler struct{ imp *Importer }

func NewHandler(imp *Importer) *Handler { return &Handler{imp: imp} }

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
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_UPLOAD", err.Error())
		return
	}
	workspaceID := r.URL.Query().Get("workspace_id")
	teamID := r.URL.Query().Get("team_id")
	if workspaceID == "" || teamID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id and team_id are required (query string)")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "NO_FILE", "expected multipart 'file' field")
		return
	}
	defer file.Close()

	out, err := fn(r.Context(), workspaceID, teamID, file)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "IMPORT_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
