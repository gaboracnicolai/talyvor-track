package guest

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
)

// issueReader is the subset of issue.Store the guest-scoped read
// endpoints call into. Keeps this package's dependency surface tiny
// and avoids a cycle if issue ever needs to import guest later.
type issueReader interface {
	List(ctx context.Context, filter issue.IssueFilter) ([]model.Issue, error)
	GetByID(ctx context.Context, id string) (*model.Issue, error)
}

type Handler struct {
	store  *Store
	issues issueReader
	// inviteBaseURL is the prefix the API stitches into the returned
	// invite_url. e.g. https://app.talyvor.com — set from config so
	// self-hosted deployments produce the right links.
	inviteBaseURL string
}

func NewHandler(store *Store, issues issueReader, inviteBaseURL string) *Handler {
	if inviteBaseURL == "" {
		inviteBaseURL = "http://localhost:5173"
	}
	return &Handler{store: store, issues: issues, inviteBaseURL: inviteBaseURL}
}

// Mount registers the admin endpoints under /v1/workspaces/{wsID}/...
// and the public invite/guest endpoints under /v1/invite/... and
// /v1/guest/... — public routes intentionally bypass member auth.
func (h *Handler) Mount(r chi.Router) {
	// Admin (member-authenticated in production).
	r.Route("/workspaces/{wsID}/guests", func(r chi.Router) {
		r.Get("/", h.List)
		r.Post("/invite", h.Invite)
		r.Delete("/{id}", h.Revoke)
	})

	// Public invite endpoints — required to accept without an account.
	r.Get("/invite/{token}", h.InviteDetail)
	r.Post("/invite/{token}/accept", h.AcceptInvite)

	// Public guest read endpoints — verified via Bearer guest token.
	r.Group(func(r chi.Router) {
		r.Use(h.store.Middleware)
		r.Use(h.store.RequireGuest)
		r.Get("/guest/workspaces/{wsID}/projects/{projectID}/issues", h.GuestListIssues)
		r.Get("/guest/workspaces/{wsID}/issues/{id}", h.GuestGetIssue)
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

// ─── admin handlers ─────────────────────────────────────────

func (h *Handler) Invite(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	actorID, ok := authz.MemberID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	var in struct {
		Email     string    `json:"email"`
		Role      GuestRole `json:"role"`
		ProjectID *string   `json:"project_id,omitempty"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if in.Role == "" {
		in.Role = GuestRoleViewer
	}
	invitedBy := actorID
	out, err := h.store.CreateInvite(r.Context(), wsID, in.ProjectID, in.Email, in.Role, invitedBy)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "INVITE_FAILED", err.Error())
		return
	}
	// Return the URL the inviter should send their guest; the raw
	// token only appears in this response (and in the row itself).
	writeJSON(w, http.StatusCreated, map[string]any{
		"invite_url": h.inviteBaseURL + "/invite/" + out.Token,
		"expires_at": out.ExpiresAt,
		"role":       out.Role,
	})
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "not a member of this workspace")
		return
	}
	var projectID *string
	if v := r.URL.Query().Get("project_id"); v != "" {
		projectID = &v
	}
	out, err := h.store.ListGuests(r.Context(), wsID, projectID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []Guest{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.store.RevokeGuest(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "REVOKE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ─── public invite handlers ─────────────────────────────────

func (h *Handler) InviteDetail(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	invite, err := h.store.GetGuestByToken(r.Context(), tok)
	if err != nil {
		writeErr(w, http.StatusNotFound, "INVITE_NOT_FOUND", err.Error())
		return
	}
	if invite.AcceptedAt != nil {
		writeErr(w, http.StatusGone, "ALREADY_ACCEPTED", "invite already accepted")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": invite.WorkspaceID,
		"project_id":   invite.ProjectID,
		"email":        invite.Email,
		"role":         invite.Role,
		"expires_at":   invite.ExpiresAt,
		"invited_by":   invite.InvitedBy,
	})
}

func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var in struct {
		Name string `json:"name"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	out, err := h.store.AcceptInvite(r.Context(), tok, in.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ACCEPT_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"guest_id":     out.Guest().ID,
		"workspace_id": out.Guest().WorkspaceID,
		"project_id":   out.Guest().ProjectID,
		"role":         out.Guest().Role,
		"access_token": out.AccessToken(),
	})
}

// ─── public guest read handlers ─────────────────────────────

func (h *Handler) GuestListIssues(w http.ResponseWriter, r *http.Request) {
	claims := Claims(r.Context())
	wsID := chi.URLParam(r, "wsID")
	projectID := chi.URLParam(r, "projectID")
	if claims.WorkspaceID != wsID {
		writeErr(w, http.StatusForbidden, "WS_MISMATCH", "workspace mismatch")
		return
	}
	if claims.ProjectID != "" && claims.ProjectID != projectID {
		writeErr(w, http.StatusForbidden, "PROJECT_MISMATCH", "project mismatch")
		return
	}
	out, err := h.issues.List(r.Context(), issue.IssueFilter{
		WorkspaceID: wsID,
		ProjectID:   projectID,
		Limit:       100,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Issue{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) GuestGetIssue(w http.ResponseWriter, r *http.Request) {
	claims := Claims(r.Context())
	wsID := chi.URLParam(r, "wsID")
	id := chi.URLParam(r, "id")
	if claims.WorkspaceID != wsID {
		writeErr(w, http.StatusForbidden, "WS_MISMATCH", "workspace mismatch")
		return
	}
	out, err := h.issues.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	// Project-scoped guests can only see issues from their project.
	if claims.ProjectID != "" {
		if out.ProjectID == nil || *out.ProjectID != claims.ProjectID {
			writeErr(w, http.StatusForbidden, "PROJECT_MISMATCH", "outside guest project scope")
			return
		}
	}
	if out.WorkspaceID != wsID {
		writeErr(w, http.StatusForbidden, "WS_MISMATCH", "workspace mismatch")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
