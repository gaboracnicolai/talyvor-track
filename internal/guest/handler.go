package guest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
)

// issueReader is the subset of issue.Store the guest-scoped endpoints call into — reads for the two GET
// routes, plus CreateComment for guest-write slice 1 (a guest commenter). Keeps this package's dependency
// surface tiny and avoids a cycle if issue ever needs to import guest later. CreateComment is a pure INSERT
// (issue/comments.go) — it fires NO member-attributed side-effect, which is why the guest path uses it
// directly (see GuestCreateComment).
type issueReader interface {
	List(ctx context.Context, filter issue.IssueFilter) ([]model.Issue, error)
	GetByID(ctx context.Context, id string) (*model.Issue, error)
	CreateComment(ctx context.Context, c model.Comment) (*model.Comment, error)
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
		// Guest-WRITE slice 1: a guest commenter (or editor) posts a comment. Same token-verify chain as the
		// reads above; the role gate + object-tenancy are enforced inside the handler.
		r.Post("/guest/workspaces/{wsID}/issues/{id}/comments", h.GuestCreateComment)
	})
}

// GuestCreateComment — guest-write slice 1: a guest COMMENTER (or editor) posts a comment on an in-scope
// issue. Full authz, in order: token-vs-URL workspace match (the .semgrep workspace-authz exemption), the
// role gate (this WIRES the previously-dead AllowWrite — a viewer is denied here), then the same
// object-tenancy as GuestGetIssue. Identity comes ONLY from the verified token claims — never a header or a
// query param.
//
// THE GUEST-ACTOR PATH (option i): the comment is stored with AuthorID = claims.GuestID, and the
// member-attributed realtime notifier is deliberately NOT fired. The guest Handler holds no notifier, and the
// member CreateComment handler's notifier.CommentCreated broadcasts an ActorID-tagged event into members'
// issue rooms — firing that with a GuestID would push a guest actor into a member-actor resolution path.
// Slice 1 stores the comment with guest attribution and skips the member-only side-effect; the comment
// surfaces on the next read. No member-assuming hook ever sees a guest/empty actor.
func (h *Handler) GuestCreateComment(w http.ResponseWriter, r *http.Request) {
	claims := Claims(r.Context())
	wsID := chi.URLParam(r, "wsID")
	if claims.WorkspaceID != wsID {
		writeErr(w, http.StatusForbidden, "WS_MISMATCH", "workspace mismatch")
		return
	}
	// ROLE GATE — wires the previously-dead AllowWrite. Viewer → denied; commenter/editor → pass.
	if !AllowWrite(claims, "comment") {
		writeErr(w, http.StatusForbidden, "INSUFFICIENT_ROLE", "guest role may not comment")
		return
	}
	id := chi.URLParam(r, "id")
	iss, err := h.issues.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	// OBJECT TENANCY — identical to GuestGetIssue: project scope, then object-in-workspace.
	if claims.ProjectID != "" {
		if iss.ProjectID == nil || *iss.ProjectID != claims.ProjectID {
			writeErr(w, http.StatusForbidden, "PROJECT_MISMATCH", "outside guest project scope")
			return
		}
	}
	if iss.WorkspaceID != wsID {
		writeErr(w, http.StatusForbidden, "WS_MISMATCH", "workspace mismatch")
		return
	}

	var in struct {
		Body string `json:"body"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if in.Body == "" {
		writeErr(w, http.StatusBadRequest, "EMPTY_BODY", "comment body is required")
		return
	}
	// AuthorID is the verified GuestID — server-set, NEVER from the client body (which carries only Body).
	out, err := h.issues.CreateComment(r.Context(), model.Comment{
		IssueID:  id,
		AuthorID: claims.GuestID,
		Body:     in.Body,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	// Deliberately NO notifier — see THE GUEST-ACTOR PATH note above.
	writeJSON(w, http.StatusCreated, out)
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
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	if err := h.store.RevokeGuest(r.Context(), chi.URLParam(r, "id"), wsID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}
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
