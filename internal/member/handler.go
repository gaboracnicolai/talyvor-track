package member

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

const (
	defaultLimit = 500
	maxLimit     = 500 // hard cap — the roster read can never return more per page
	bearerPrefix = "Bearer "
)

// Handler serves GET /v1/service/members. The route is gwExempt (it skips the gateway
// transit-proof + membership authz), so the handler does its OWN service auth: a bearer
// token constant-time-compared against the configured secret. secret=="" ⇒ the endpoint
// 401s ALL requests (member-sync disabled) — the highest-value data never served open.
type Handler struct {
	store  *Store
	secret string
}

func NewHandler(store *Store, secret string) *Handler {
	return &Handler{store: store, secret: secret}
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/service/members", h.List)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid or missing service token")
		return
	}
	// SEMGREP EXEMPTION (reviewed): the caller-workspace-id-query-needs-authorization rule wants this routed
	// through authz.AuthorizeWorkspace(ctx, workspaceID) → 403-if-not-a-member. That is INAPPLICABLE here by
	// design: this is a gwExempt SERVICE endpoint whose caller is a trusted service principal (the Docs
	// member-sync), NOT a user with a per-workspace membership. Authorization is the constant-time bearer
	// check in authorized() above — a valid token may read any workspace's roster (the accepted service-
	// credential posture). Per-user membership authz does not exist for this caller, so the rule's fix cannot
	// apply; the query read is still workspace-SCOPED (WHERE workspace_id=$1) and required (400 if empty).
	// INVALIDATED IF: this route stops being gwExempt / becomes user-facing; OR authorized() stops running
	// FIRST in List (the token gate must precede the query read); OR h.secret gains a non-empty default
	// (authorized() would no longer fail-closed on an unset secret); OR the workspace_id query param becomes
	// optional or the read stops being scoped by WHERE workspace_id=$1 — any of these turns a valid token into
	// a mass cross-workspace roster read.
	workspaceID := r.URL.Query().Get("workspace_id") // nosemgrep: caller-workspace-id-query-needs-authorization
	if workspaceID == "" {
		// A scoped read REQUIRES an explicit workspace — never a full-table dump.
		auditPull(workspaceID, 0, http.StatusBadRequest)
		writeErr(w, http.StatusBadRequest, "BAD_PARAMS", "workspace_id is required")
		return
	}
	limit, offset := pageParams(r)
	members, err := h.store.ListWorkspaceMembers(r.Context(), workspaceID, limit, offset)
	if err != nil {
		auditPull(workspaceID, 0, http.StatusInternalServerError)
		writeErr(w, http.StatusInternalServerError, "LIST_FAILED", err.Error())
		return
	}
	// A non-existent workspace_id simply yields an empty roster (200) — and is audited
	// the same way (count=0) so sequential-ID probing by a leaked token is VISIBLE.
	auditPull(workspaceID, len(members), http.StatusOK)
	writeJSON(w, http.StatusOK, members)
}

// auditPull emits the CONTAINMENT signal for every post-auth pull: a leaked token doing
// mass cross-workspace enumeration must be visible in logs. It logs the workspace_id, the
// row COUNT (never the roster — a log that copies emails is a second leak), and the
// outcome, under a stable event marker.
func auditPull(workspaceID string, count, status int) {
	slog.Info("service member pull",
		slog.String("event", "service_member_pull"),
		slog.String("workspace_id", workspaceID),
		slog.Int("count", count),
		slog.Int("status", status),
	)
}

// authorized constant-time-compares the bearer token's digest against the secret's.
// Unset secret ⇒ never authorized (refuses all). Mirrors gatewayauth's static-secret
// digest compare — a GET has no body, so HMAC-over-body does not apply.
func (h *Handler) authorized(r *http.Request) bool {
	if h.secret == "" {
		return false
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, bearerPrefix) {
		return false
	}
	token := strings.TrimPrefix(auth, bearerPrefix)
	got := sha256.Sum256([]byte(token))
	want := sha256.Sum256([]byte(h.secret))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func pageParams(r *http.Request) (limit, offset int) {
	limit, offset = defaultLimit, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}
