package project

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

// authorizedWorkspace returns the caller's server-authorized workspace (from the authz
// middleware). ok=false ⇒ the handler must refuse — the SEC-5 scope source, never the URL/body.
func authorizedWorkspace(r *http.Request) (string, bool) { return authz.WorkspaceID(r.Context()) }

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/projects", func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/", h.List)
		r.Get("/{id}", h.Get)
		r.Patch("/{id}", h.Update)
		r.Delete("/{id}", h.Delete)
	})

	// Roadmap query lives at /workspaces/{wsID}/roadmap so the URL
	// matches the resource concept (timeline-of-projects) rather than
	// being buried under /projects/roadmap.
	r.Get("/workspaces/{wsID}/roadmap", h.Roadmap)
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
	var in model.Project
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	in.WorkspaceID = wsID
	out, err := h.store.Create(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no authorized workspace")
		return
	}
	out, err := h.store.ListByWorkspace(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Project{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.GetByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	var updates map[string]any
	if !httpx.DecodeJSON(w, r, &updates) {
		return
	}
	wsID, ok := authorizedWorkspace(r)
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
	wsID, ok := authorizedWorkspace(r)
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
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

// Roadmap drives the timeline page. Defaults: today → today+6mo. Both
// dates may be overridden via ?start_date= and ?end_date= as RFC3339;
// ?team_id= scopes the roadmap to a single team.
func (h *Handler) Roadmap(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "no authorized workspace")
		return
	}
	now := time.Now().UTC()
	start := now
	end := now.AddDate(0, 6, 0)
	if v := r.URL.Query().Get("start_date"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			start = t
		}
	}
	if v := r.URL.Query().Get("end_date"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			end = t
		}
	}
	var teamID *string
	if v := r.URL.Query().Get("team_id"); v != "" {
		teamID = &v
	}

	projects, err := h.store.GetRoadmap(r.Context(), wsID, teamID, start, end)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ROADMAP_FAILED", err.Error())
		return
	}
	if projects == nil {
		projects = []RoadmapProject{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": projects,
		"date_range": map[string]string{
			"start": start.Format(time.RFC3339),
			"end":   end.Format(time.RFC3339),
		},
	})
}
