package member

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
)

// MgmtHandler serves the owner-gated member-management API at
// /v1/workspaces/{wsID}/members. Distinct from Handler (the gwExempt service roster read):
// these are ordinary {wsID} routes behind gwAuth + wsAuthz, and EVERY one requires the
// caller be an OWNER of the workspace — the member analogue of guest AllowWrite, reading
// the role wsAuthz already resolved into context (never a token, never a body field).
type MgmtHandler struct{ store *Store }

func NewMgmtHandler(store *Store) *MgmtHandler { return &MgmtHandler{store: store} }

func (h *MgmtHandler) Mount(r chi.Router) {
	// No trailing slash — avoids the StripSlashes-absent 404 trap other collection routes hit.
	r.Get("/workspaces/{wsID}/members", h.List)
	r.Post("/workspaces/{wsID}/members", h.Add)
	r.Patch("/workspaces/{wsID}/members/{id}", h.ChangeRole)
	r.Delete("/workspaces/{wsID}/members/{id}", h.Remove)
}

// requireOwner is the single owner gate for every member-management op. wsID comes from
// the server-AUTHORIZED context (set by wsAuthz after proving membership of {wsID}), never
// the URL param; the role is likewise the resolved ctx role. Fail-closed via authz.IsOwner.
func (h *MgmtHandler) requireOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return "", false
	}
	if !authz.IsOwner(r.Context()) {
		writeErr(w, http.StatusForbidden, "OWNER_REQUIRED", "owner role required")
		return "", false
	}
	return wsID, true
}

// memberView is the picker projection List returns — exactly what an assignee/@mention/
// reviewer dropdown needs (id, name, email, role, avatar_url) and nothing more. It omits
// model.Member's workspace_id (the caller already has it from the path) and created_at, so
// the roster read never leaks beyond what the frontend needs. avatar_url IS included: a
// picker shows avatars, Track already stores the field, and it is not sensitive (members can
// already see each other's names and emails).
type memberView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	AvatarURL string `json:"avatar_url"`
}

// List returns the workspace roster. Readable by ANY member of the workspace: wsAuthz has
// already proved membership of {wsID} (a non-member is 403'd WORKSPACE_FORBIDDEN before this
// handler), so this needs membership but NOT owner — you may see who is in your workspace.
// Only the three WRITES (Add/ChangeRole/Remove) are owner-gated.
func (h *MgmtHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	members, err := h.store.ListMembers(r.Context(), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	out := make([]memberView, 0, len(members))
	for _, m := range members {
		out = append(out, memberView{ID: m.ID, Name: m.Name, Email: m.Email, Role: m.Role, AvatarURL: m.AvatarURL})
	}
	writeJSON(w, http.StatusOK, out)
}

type addMemberBody struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (h *MgmtHandler) Add(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	var in addMemberBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	if in.Email == "" {
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "email is required")
		return
	}
	// Default the role EXPLICITLY to member — the INSERT never leans on the DB default
	// (lockout hazard a). An off-tier role is rejected by the store (ErrInvalidRole).
	role := in.Role
	if role == "" {
		role = authz.RoleMember
	}
	m, err := h.store.AddMember(r.Context(), wsID, in.Email, role)
	switch {
	case errors.Is(err, ErrInvalidRole):
		writeErr(w, http.StatusBadRequest, "INVALID_ROLE", err.Error())
	case errors.Is(err, ErrMemberExists):
		writeErr(w, http.StatusConflict, "MEMBER_EXISTS", err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "ADD_FAILED", err.Error())
	default:
		writeJSON(w, http.StatusCreated, m)
	}
}

type changeRoleBody struct {
	Role string `json:"role"`
}

func (h *MgmtHandler) ChangeRole(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	var in changeRoleBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}
	m, err := h.store.ChangeRole(r.Context(), wsID, chi.URLParam(r, "id"), in.Role)
	switch {
	case errors.Is(err, ErrInvalidRole):
		writeErr(w, http.StatusBadRequest, "INVALID_ROLE", err.Error())
	case errors.Is(err, ErrMemberNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, ErrLastOwner):
		writeErr(w, http.StatusConflict, "LAST_OWNER", err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "CHANGE_FAILED", err.Error())
	default:
		writeJSON(w, http.StatusOK, m)
	}
}

func (h *MgmtHandler) Remove(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.requireOwner(w, r)
	if !ok {
		return
	}
	err := h.store.RemoveMember(r.Context(), wsID, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, ErrMemberNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, ErrLastOwner):
		writeErr(w, http.StatusConflict, "LAST_OWNER", err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "REMOVE_FAILED", err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
