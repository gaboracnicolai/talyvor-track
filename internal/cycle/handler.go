package cycle

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/model"
)

// emailer is the subset of the notification dispatcher the cycle handler calls.
// Local interface so the cycle package stays free of a notification import;
// email is optional and opt-in. Calls are best-effort and never fail the request.
type emailer interface {
	SprintStarted(ctx context.Context, cycle model.Cycle, actorID string)
	SprintEnded(ctx context.Context, cycle model.Cycle, actorID string)
}

type Handler struct {
	store   *Store
	emailer emailer
}

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// WithEmailer wires the email dispatcher. Optional/opt-in: without it, cycle
// behaviour is unchanged.
func (h *Handler) WithEmailer(e emailer) *Handler {
	h.emailer = e
	return h
}

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
	var in model.Cycle
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	in.WorkspaceID = chi.URLParam(r, "wsID")
	in.TeamID = chi.URLParam(r, "teamID")
	out, err := h.store.Create(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	// Track has no separate cycle-activation flow today (Create defaults to
	// "upcoming"; PATCH is 501), so a "sprint started" can only be observed
	// when a cycle is created already-active. When an activation flow is added,
	// it should call SprintStarted too.
	if h.emailer != nil && out.Status == "active" {
		h.emailer.SprintStarted(r.Context(), *out, r.Header.Get("X-Member-Id"))
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

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	// Cycle updates are limited to name/status/end_date; a future
	// phase can grow this. For now we return 501 so the route shape
	// is documented but no caller can corrupt cycle metadata.
	writeErr(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "cycle PATCH not implemented in phase 2")
}

func (h *Handler) Complete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Load before completing so the email can carry the sprint's name (Complete
	// only takes an ID). Best-effort: a load failure just means no email.
	var cyc *model.Cycle
	if h.emailer != nil {
		cyc, _ = h.store.GetByID(r.Context(), id)
	}
	if err := h.store.Complete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "COMPLETE_FAILED", err.Error())
		return
	}
	if h.emailer != nil && cyc != nil {
		h.emailer.SprintEnded(r.Context(), *cyc, r.Header.Get("X-Member-Id"))
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
