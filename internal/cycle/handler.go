package cycle

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/model"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/teams/{teamID}/cycles", func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/", h.List)
		r.Get("/active", h.GetActive)
		r.Patch("/{id}", h.Update)
		r.Post("/{id}/complete", h.Complete)
		r.Get("/{id}/progress", h.Progress)
		r.Get("/{id}/burndown", h.Burndown)
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

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no authorized workspace")
		return
	}
	var in model.Cycle
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	in.WorkspaceID = wsID
	in.TeamID = chi.URLParam(r, "teamID")
	out, err := h.store.Create(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListByTeam(r.Context(), chi.URLParam(r, "teamID"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Cycle{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) GetActive(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.GetActive(r.Context(), chi.URLParam(r, "teamID"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ACTIVE_FAILED", err.Error())
		return
	}
	if out == nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "no active cycle", Code: "NO_ACTIVE_CYCLE"})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Update applies a partial update (name / status / start_date / end_date) to a cycle,
// scoped to the workspace in the path. Unknown or cross-workspace cycles → 404.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no authorized workspace")
		return
	}
	id := chi.URLParam(r, "id")
	var in struct {
		Name      *string    `json:"name"`
		Status    *string    `json:"status"`
		StartDate *time.Time `json:"start_date"`
		EndDate   *time.Time `json:"end_date"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	out, err := h.store.Update(r.Context(), id, wsID, CycleUpdate{
		Name: in.Name, Status: in.Status, StartDate: in.StartDate, EndDate: in.EndDate,
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "CYCLE_NOT_FOUND", err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Complete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Complete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "COMPLETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) Progress(w http.ResponseWriter, r *http.Request) {
	p, err := h.store.GetProgress(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "PROGRESS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) Burndown(w http.ResponseWriter, r *http.Request) {
	pts, err := h.store.GetBurndown(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "BURNDOWN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pts)
}
