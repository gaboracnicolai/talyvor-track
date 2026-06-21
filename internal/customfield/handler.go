package customfield

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
)

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// Mount registers both the field-catalogue endpoints
// (/workspaces/{wsID}/custom-fields/*) and the per-issue value
// endpoints (/workspaces/{wsID}/issues/{id}/fields/*).
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/custom-fields", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Patch("/{id}", h.Update)
		r.Delete("/{id}", h.Delete)
	})

	r.Route("/workspaces/{wsID}/issues/{id}/fields", func(r chi.Router) {
		r.Get("/", h.GetValues)
		r.Put("/{fieldID}", h.SetValue)
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
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	var teamID *string
	if v := r.URL.Query().Get("team_id"); v != "" {
		teamID = &v
	}
	fields, err := h.store.ListFields(r.Context(), wsID, teamID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if fields == nil {
		fields = []CustomField{}
	}
	writeJSON(w, http.StatusOK, fields)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	var in CustomField
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	in.WorkspaceID = wsID
	out, err := h.store.CreateField(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Name     string   `json:"name"`
		Options  []string `json:"options"`
		Required bool     `json:"required"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	out, err := h.store.UpdateField(r.Context(), id, in.Name, in.Options, in.Required)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.DeleteField(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) GetValues(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	values, err := h.store.GetValues(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "GET_VALUES_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, values)
}

func (h *Handler) SetValue(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	fieldID := chi.URLParam(r, "fieldID")
	var in struct {
		Value string `json:"value"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if err := h.store.SetValue(r.Context(), id, fieldID, in.Value); err != nil {
		writeErr(w, http.StatusBadRequest, "SET_VALUE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
