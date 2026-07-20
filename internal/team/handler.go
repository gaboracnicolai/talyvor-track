package team

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/model"
)

// workflowSeeder is the subset of workflow.Engine the team handler
// uses to bootstrap default statuses on team creation. Defined
// locally as an interface so the team package doesn't depend on the
// workflow package directly (would risk an import cycle later).
type workflowSeeder interface {
	SeedDefaults(ctx context.Context, teamID string) error
}

type Handler struct {
	store  *Store
	seeder workflowSeeder
}

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// WithSeeder attaches a workflow seeder so every new team comes up
// with the six default statuses already configured. Without it, the
// team is created with an empty status set and callers must populate
// it explicitly via the workflow API.
func (h *Handler) WithSeeder(s workflowSeeder) *Handler {
	h.seeder = s
	return h
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/teams", func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/", h.List)
		r.Get("/{id}", h.Get)
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

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "WORKSPACE_FORBIDDEN", "not a member of this workspace")
		return
	}
	var in model.Team
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	in.WorkspaceID = wsID
	out, err := h.store.Create(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	// Seed default workflow statuses. Best-effort: if seeding fails
	// the team is still created — operators can populate statuses
	// manually via the workflow API.
	if h.seeder != nil {
		if err := h.seeder.SeedDefaults(r.Context(), out.ID); err != nil {
			slog.Warn("team: seed default statuses failed",
				slog.String("team_id", out.ID),
				slog.String("err", err.Error()),
			)
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "WORKSPACE_FORBIDDEN", "not a member of this workspace")
		return
	}
	out, err := h.store.ListByWorkspace(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Team{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	// SEC-5: scoped read — foreign id → ErrNotFound → 404 (no disclosure, no oracle).
	out, err := h.store.getInWorkspace(r.Context(), chi.URLParam(r, "id"), wsID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var updates map[string]any
	if !httpx.DecodeJSON(w, r, &updates) {
		return
	}
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	out, err := h.store.Update(r.Context(), chi.URLParam(r, "id"), wsID, updates)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if !authz.IsOwner(r.Context()) { // owner-gated: deleting a team
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")
		return
	}
	if err := h.store.Delete(r.Context(), chi.URLParam(r, "id"), wsID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
