package workflow

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

type Handler struct{ engine *Engine }

func NewHandler(engine *Engine) *Handler { return &Handler{engine: engine} }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/teams/{teamID}/statuses", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Patch("/{id}", h.Update)
		r.Delete("/{id}", h.Delete)
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

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	out, err := h.engine.GetStatuses(r.Context(), chi.URLParam(r, "teamID"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []WorkflowStatus{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var in WorkflowStatus
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	in.TeamID = chi.URLParam(r, "teamID")
	out, err := h.engine.CreateStatus(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Color    string `json:"color"`
		Position int    `json:"position"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	out, err := h.engine.UpdateStatus(r.Context(), chi.URLParam(r, "id"), in.Name, in.Color, in.Position)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.engine.DeleteStatus(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusConflict, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// avoid unused import warning if file evolves
var _ = strconv.Atoi
