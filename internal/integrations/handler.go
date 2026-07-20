package integrations

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
)

// handler.go — the config surface for a workspace to SET its provider integration. Behind the same
// authz.AuthorizeWorkspace gate as the rest of /v1, tenancy-scoped to the caller's workspace. The token
// arrives in the request body (over TLS) and is NEVER echoed back or exposed by any read.

type Handler struct{ store *Store }

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

const maxIntegrationBody = 1 << 20 // 1 MiB — a token payload is tiny

var validProviders = map[string]bool{"linear": true, "jira": true}

func (h *Handler) Mount(r chi.Router) {
	r.Post("/integrations", h.set)
	r.Get("/integrations/{provider}", h.status)
}

type setRequest struct {
	Provider   string `json:"provider"`
	Token      string `json:"token"`
	ProjectKey string `json:"project_or_team_key"`
	BaseURL    string `json:"base_url"`
}

// set stores (encrypted) a workspace's provider token. authz-gated; the workspace written is the
// server-resolved m.WorkspaceID (never the raw query). The response is the NON-SECRET Integration view —
// it does NOT echo the token.
func (h *Handler) set(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id required (query)")
		return
	}
	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	// Owner-gated: this writes a live provider credential. Flat route (no ctx role), so gate
	// on the role of the membership AuthorizeWorkspace just resolved.
	if !authz.IsOwnerRole(m.Role) {
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")
		return
	}
	var in setRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIntegrationBody)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if !validProviders[in.Provider] {
		writeErr(w, http.StatusBadRequest, "BAD_PROVIDER", "provider must be linear or jira")
		return
	}
	if in.Token == "" {
		writeErr(w, http.StatusBadRequest, "NO_TOKEN", "token is required")
		return
	}
	id, err := h.store.Upsert(r.Context(), m.WorkspaceID, in.Provider, in.Token, in.ProjectKey, in.BaseURL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "STORE_FAILED", err.Error())
		return
	}
	// Response NEVER contains the token — only the non-secret view.
	writeJSON(w, http.StatusCreated, Integration{
		ID: id, Provider: in.Provider, ProjectKey: in.ProjectKey, BaseURL: in.BaseURL, Configured: true,
	})
}

// status returns the NON-SECRET view (provider/project/base_url/configured) — never the token/ciphertext.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id required (query)")
		return
	}
	m, ok := authz.AuthorizeWorkspace(r.Context(), workspaceID)
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	provider := chi.URLParam(r, "provider")
	in, err := h.store.Get(r.Context(), m.WorkspaceID, provider)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LOOKUP_FAILED", err.Error())
		return
	}
	if in == nil {
		writeJSON(w, http.StatusOK, Integration{Provider: provider, Configured: false})
		return
	}
	writeJSON(w, http.StatusOK, in) // Integration has no token field
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}
