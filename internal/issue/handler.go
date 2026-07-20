package issue

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/httpx"
	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/model"
)

// notifier is the subset of internal/realtime.Notifier the issue
// handler depends on. Defined locally as an interface so the issue
// package doesn't import realtime — the WS infrastructure stays
// optional and the import graph stays simple.
type notifier interface {
	IssueCreated(ctx context.Context, wsID, teamID, actorID string, issue model.Issue)
	IssueUpdated(ctx context.Context, wsID, teamID, issueID, actorID string, changes map[string]any)
	IssueDeleted(ctx context.Context, wsID, teamID, issueID, actorID string)
	CommentCreated(ctx context.Context, wsID, issueID, actorID string, comment model.Comment)
	CommentUpdated(ctx context.Context, wsID, issueID, actorID string, comment model.Comment)
	CommentDeleted(ctx context.Context, wsID, issueID, commentID, actorID string)
}

// automationFirer is the subset of internal/automation.Engine the
// issue handler calls. The local interface keeps the issue package
// from importing automation directly. The trigger argument is a
// string to avoid pulling the automation.RuleTrigger type into the
// issue package's API.
type automationFirer interface {
	Fire(ctx context.Context, trigger string, workspaceID string, issue model.Issue, changes map[string]any) error
}

// customFields is the subset of customfield.Store the issue handler
// uses on Create: validate required fields and persist any values
// supplied in the request body. Kept local to avoid importing the
// customfield package directly into issue.
type customFields interface {
	ValidateRequired(ctx context.Context, workspaceID string, teamID *string, provided map[string]string) error
	SetValue(ctx context.Context, issueID, fieldID, workspaceID, value string) error
}

// templateApplier merges an IssueTemplate's defaults into an Issue.
// The full interface only needs GetByID (lookup) and Apply
// (mutation) — we keep ApplyTo on the template type itself so this
// interface stays narrow.
type templateApplier interface {
	ApplyTemplate(ctx context.Context, templateID string, into *model.Issue) error
}

// Handler is the HTTP surface for /workspaces/{wsID}/issues/*.
type Handler struct {
	store        *Store
	notifier     notifier
	automation   automationFirer
	customFields customFields
	templates    templateApplier
}

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// WithNotifier wires the realtime notifier so every successful issue
// or comment mutation fans out over WebSockets. Optional — without
// it, the handler is fully functional but no live updates fire.
func (h *Handler) WithNotifier(n notifier) *Handler {
	h.notifier = n
	return h
}

// WithAutomation wires the automation engine. Issue lifecycle events
// fire matching rules synchronously after the DB write completes.
// Rule failures never fail the triggering request.
func (h *Handler) WithAutomation(a automationFirer) *Handler {
	h.automation = a
	return h
}

// WithCustomFields wires the custom-field bridge. When set, Create
// enforces required-field validation and persists the values present
// in the POST body's field_values map.
func (h *Handler) WithCustomFields(c customFields) *Handler {
	h.customFields = c
	return h
}

// WithTemplates wires the template applier. When set, Create accepts
// an optional template_id; missing templates fall back to a blank
// issue silently rather than failing the request.
func (h *Handler) WithTemplates(t templateApplier) *Handler {
	h.templates = t
	return h
}

// Mount registers every issue route on r. Routes are mounted under
// /workspaces/{wsID}/issues so the workspace ID is always part of the
// URL — multi-tenant scoping is enforced at the route level, not by
// trusting a header.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/workspaces/{wsID}/issues", func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/", h.List)
		r.Get("/search", h.Search)
		// bulk-update sits above the {id} pattern so chi resolves the
		// literal path before the wildcard. Reorder with care: kanban
		// drag-and-drop relies on this endpoint to apply column moves
		// atomically.
		r.Patch("/bulk-update", h.BulkUpdate)
		r.Get("/{id}", h.Get)
		r.Patch("/{id}", h.Update)
		r.Delete("/{id}", h.Delete)

		// Comments live under the issue. The actor is the caller's
		// resolved member id from the authz context (T10), never a
		// caller-supplied header.
		r.Post("/{id}/comments", h.CreateComment)
		r.Get("/{id}/comments", h.ListComments)
		r.Patch("/{id}/comments/{commentID}", h.UpdateComment)
		r.Delete("/{id}/comments/{commentID}", h.DeleteComment)
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

// createBody mirrors model.Issue plus the optional template_id input
// that's only meaningful at create time. Keeping it separate lets
// the model stay focused on persisted shape.
type createBody struct {
	model.Issue
	TemplateID string `json:"template_id,omitempty"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	var body createBody
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	in := body.Issue
	in.WorkspaceID = wsID

	// Apply the template's defaults before validation runs so any
	// required custom-field values seeded by the template count
	// toward the required-field check.
	if body.TemplateID != "" && h.templates != nil {
		if err := h.templates.ApplyTemplate(r.Context(), body.TemplateID, &in); err != nil {
			// "Templates never block issue creation" — swallow the
			// error and continue with whatever the caller provided.
			_ = err
		}
	}

	// Custom-field required-field validation runs before the issue is
	// inserted so a missing value fails fast and doesn't leave half-
	// stamped state behind. Skipped when no bridge is wired.
	if h.customFields != nil {
		var teamID *string
		if in.TeamID != "" {
			t := in.TeamID
			teamID = &t
		}
		if err := h.customFields.ValidateRequired(r.Context(), wsID, teamID, in.FieldValues); err != nil {
			writeErr(w, http.StatusBadRequest, "REQUIRED_FIELD_MISSING", err.Error())
			return
		}
	}

	// Strip FieldValues from the Issue before insert — they belong in
	// issue_field_values, not the issues row. We re-attach below.
	provided := in.FieldValues
	in.FieldValues = nil

	out, err := h.store.Create(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}

	// Persist supplied field values. Per-field failure aborts so the
	// caller sees the first validation problem instead of silent
	// drops; the issue itself stays — the user can retry SetValue.
	if h.customFields != nil && len(provided) > 0 {
		for fieldID, value := range provided {
			// out.WorkspaceID is the just-created issue's workspace (the caller's authorized one).
			if err := h.customFields.SetValue(r.Context(), out.ID, fieldID, out.WorkspaceID, value); err != nil {
				writeErr(w, http.StatusBadRequest, "FIELD_VALUE_FAILED", err.Error())
				return
			}
		}
		out.FieldValues = provided
	}

	metrics.IssuesCreated.WithLabelValues(out.WorkspaceID, out.TeamID, string(out.Status)).Inc()
	if h.notifier != nil {
		h.notifier.IssueCreated(r.Context(), out.WorkspaceID, out.TeamID, out.CreatorID, *out)
	}
	if h.automation != nil {
		_ = h.automation.Fire(r.Context(), "issue.created", out.WorkspaceID, *out, nil)
	}
	writeJSON(w, http.StatusCreated, out)
}

// List handles GET /workspaces/{wsID}/issues with optional query
// params: status, team_id, project_id, cycle_id, assignee_id,
// priority, labels, limit, offset, order_by, order_dir.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	q := r.URL.Query()
	filter := IssueFilter{
		WorkspaceID: wsID,
		TeamID:      q.Get("team_id"),
		ProjectID:   q.Get("project_id"),
		CycleID:     q.Get("cycle_id"),
		Status:      q.Get("status"),
		AssigneeID:  q.Get("assignee_id"),
		OrderBy:     q.Get("order_by"),
		OrderDir:    q.Get("order_dir"),
	}
	if v := q.Get("priority"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Priority = n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Offset = n
		}
	}

	out, err := h.store.List(r.Context(), filter)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Issue{}
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
	id := chi.URLParam(r, "id")
	var updates map[string]any
	if !httpx.DecodeJSON(w, r, &updates) {
		return
	}
	if len(updates) == 0 {
		writeErr(w, http.StatusBadRequest, "EMPTY_UPDATE", "no fields provided")
		return
	}
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	out, err := h.store.Update(r.Context(), id, wsID, updates)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	metrics.IssuesUpdated.WithLabelValues(out.WorkspaceID, out.TeamID, string(out.Status)).Inc()
	if h.notifier != nil {
		actorID, ok := authz.MemberID(r.Context())
		if !ok {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
			return
		}
		h.notifier.IssueUpdated(r.Context(), out.WorkspaceID, out.TeamID, out.ID, actorID, updates)
	}
	if h.automation != nil {
		// Fire the generic issue.updated trigger plus any specific
		// triggers implied by the changed fields. This lets users
		// write narrow rules like "fire only when status changes".
		_ = h.automation.Fire(r.Context(), "issue.updated", out.WorkspaceID, *out, updates)
		if _, ok := updates["status"]; ok {
			_ = h.automation.Fire(r.Context(), "status.changed", out.WorkspaceID, *out, updates)
		}
		if _, ok := updates["assignee_id"]; ok {
			_ = h.automation.Fire(r.Context(), "assignee.changed", out.WorkspaceID, *out, updates)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	// SEC-5: soft-cancel scoped to the caller's authorized workspace. A foreign id → 404,
	// never a cross-tenant cancel — the scope check runs BEFORE we read the issue for the
	// notifier (so a foreign issue is never even fetched for broadcast).
	if err := h.store.Delete(r.Context(), id, wsID); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	// Broadcast: the issue is in-workspace (Delete succeeded); status is "cancelled" but
	// team_id is intact, so we can resolve the team room.
	if h.notifier != nil {
		if existing, _ := h.store.GetByID(r.Context(), id); existing != nil {
			if actorID, okA := authz.MemberID(r.Context()); okA {
				h.notifier.IssueDeleted(r.Context(), existing.WorkspaceID, existing.TeamID, id, actorID)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// CreateComment appends a comment to an issue and fans out a
// comment.created event to the issue's room. The author_id is the
// caller's resolved member id from the authz context (T10).
func (h *Handler) CreateComment(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	actorID, ok := authz.MemberID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	issueID := chi.URLParam(r, "id")
	var in model.Comment
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	in.IssueID = issueID
	// SEC-5 (identity): the author is ALWAYS the verified session member — a supplied author_id
	// is ignored, so no caller can attribute a comment to another member (SEC-4 forged-actor class).
	in.AuthorID = actorID
	// SEC-5 (tenancy): scope the write to the caller's authorized workspace — a comment on a
	// foreign issue is refused (ErrNotFound → 404, no-oracle), never written cross-tenant.
	out, err := h.store.CreateComment(r.Context(), in, wsID)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CREATE_FAILED", err.Error())
		return
	}
	if h.notifier != nil {
		h.notifier.CommentCreated(r.Context(), wsID, issueID, out.AuthorID, *out)
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) ListComments(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	out, err := h.store.ListComments(r.Context(), chi.URLParam(r, "id"), wsID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Comment{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) UpdateComment(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	callerID, ok := authz.MemberID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	// author-or-owner: only the author or a workspace owner may edit.
	out, err := h.store.UpdateComment(r.Context(), chi.URLParam(r, "commentID"), wsID, callerID, in.Body, authz.IsOwner(r.Context()))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "UPDATE_FAILED", err.Error())
		return
	}
	if h.notifier != nil {
		actorID, ok := authz.MemberID(r.Context())
		if !ok {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
			return
		}
		h.notifier.CommentUpdated(r.Context(), wsID, chi.URLParam(r, "id"), actorID, *out)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	callerID, ok := authz.MemberID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	commentID := chi.URLParam(r, "commentID")
	// author-or-owner: only the author or a workspace owner may delete.
	if err := h.store.DeleteComment(r.Context(), commentID, wsID, callerID, authz.IsOwner(r.Context())); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
		return
	}
	if h.notifier != nil {
		actorID, ok := authz.MemberID(r.Context())
		if !ok {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
			return
		}
		h.notifier.CommentDeleted(r.Context(), wsID, chi.URLParam(r, "id"), commentID, actorID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_QUERY", "q query parameter is required")
		return
	}
	limit := 25
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	out, err := h.store.Search(r.Context(), wsID, query, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "SEARCH_FAILED", err.Error())
		return
	}
	if out == nil {
		out = []model.Issue{}
	}
	writeJSON(w, http.StatusOK, out)
}

// BulkUpdate applies many status / sort_order patches in one tx.
// The kanban board calls this on every drop: every card whose
// position shifts ships in one request so the board never renders
// half-applied state across a network round-trip.
func (h *Handler) BulkUpdate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Updates []BulkUpdateItem `json:"updates"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if len(in.Updates) == 0 {
		writeJSON(w, http.StatusOK, map[string]int{"updated": 0})
		return
	}
	// Sanity-check the batch size. A single drag should produce at
	// most a column's worth of sort_order shifts; anything over 500
	// is almost certainly a misuse.
	const maxBatch = 500
	if len(in.Updates) > maxBatch {
		writeErr(w, http.StatusBadRequest, "BATCH_TOO_LARGE",
			"updates array exceeds max size")
		return
	}
	// ITEM A: scope the batch to the caller's verified workspace (mirrors the single Update handler),
	// so a member of workspace A cannot flip another workspace's issues by id.
	wsID, ok := authz.WorkspaceID(r.Context())
	if !ok {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "workspace not authorized")
		return
	}
	count, err := h.store.BulkUpdate(r.Context(), wsID, in.Updates)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "BULK_UPDATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": count})
}

// avoid unused import warnings while we wire ancillary error types
var _ = errors.New
