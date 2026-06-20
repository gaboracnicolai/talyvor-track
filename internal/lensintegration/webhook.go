package lensintegration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/notification"
)

// notifier is the slice of internal/realtime.Notifier the webhook
// needs. Local interface keeps the package boundary clean.
type notifier interface {
	IssueUpdated(ctx context.Context, wsID, teamID, issueID, actorID string, changes map[string]any)
}

// issueLookup is the read side of internal/issue.Store the webhook
// uses to find which issue + assignee to notify. Defined here as an
// interface so tests can substitute a counter mock.
type issueLookup interface {
	GetByIdentifier(ctx context.Context, identifier string) (*model.Issue, error)
	RecordSpendEvent(ctx context.Context, eventKey, lensFeature string, costUSD float64, tokens int, workspaceID, source string) (int, error)
}

// notificationCreator is the slice of notification.Store the webhook
// needs — just inserting one row per spend alert.
type notificationCreator interface {
	Create(ctx context.Context, n notification.Notification) (*notification.Notification, error)
}

// WebhookHandler receives Lens → Track push events. The shared secret
// gates every request — Lens HMAC-signs the body, the handler
// verifies before acting. Without the secret nothing else fires.
type WebhookHandler struct {
	secret        string
	issues        issueLookup
	notifications notificationCreator
	notifier      notifier
}

func NewWebhookHandler(secret string, issues issueLookup, notifications notificationCreator, notif notifier) *WebhookHandler {
	return &WebhookHandler{
		secret:        secret,
		issues:        issues,
		notifications: notifications,
		notifier:      notif,
	}
}

// SpendAlertPayload is the documented body shape for Lens spend
// alerts. New fields can be added without breaking older Track
// receivers — JSON decode ignores unknowns by default.
type SpendAlertPayload struct {
	Type        string  `json:"type"`
	WorkspaceID string  `json:"workspace_id"`
	Feature     string  `json:"feature"`
	CostUSD     float64 `json:"cost_usd"`
	Threshold   float64 `json:"threshold"`
}

// ServeHTTP is the chi handler. Mount at POST /v1/lens/webhook.
// Always responds 200 on success, 401 on signature mismatch, 400 on
// malformed body. The handler never returns 5xx for transient
// downstream failures — Lens shouldn't retry on those.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_BODY", err.Error())
		return
	}
	if h.secret == "" {
		// Operator hasn't configured the shared secret. We refuse
		// the request rather than process unsigned input — locking
		// down by default protects against deploys where the
		// webhook URL leaked but auth wasn't set.
		writeErr(w, http.StatusUnauthorized, "WEBHOOK_DISABLED", "webhook secret not configured")
		return
	}
	sig := r.Header.Get("X-Lens-Signature")
	if !verifySignature(body, sig, h.secret) {
		writeErr(w, http.StatusUnauthorized, "BAD_SIGNATURE", "invalid HMAC signature")
		return
	}

	// Peek at the type first — different payloads have different shapes.
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
		return
	}

	switch head.Type {
	case "spend_alert":
		var payload SpendAlertPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			writeErr(w, http.StatusBadRequest, "BAD_JSON", err.Error())
			return
		}
		// Idempotency key = hash of the exact signed body. A re-delivered event
		// (identical bytes) maps to the same key and is recorded exactly once.
		sum := sha256.Sum256(body)
		eventKey := "lens-spend:" + hex.EncodeToString(sum[:])
		if err := h.handleSpendAlert(r.Context(), payload, eventKey); err != nil {
			slog.Warn("lensintegration: spend alert handling failed",
				slog.String("workspace_id", payload.WorkspaceID),
				slog.String("feature", payload.Feature),
				slog.String("err", err.Error()),
			)
		}
	default:
		slog.Info("lensintegration: ignoring unknown webhook type",
			slog.String("type", head.Type),
		)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleSpendAlert is the spend_alert side-effect bundle:
//
//  1. Add the cost to the issue's running total.
//  2. Look up the assignee and create a notification.
//  3. Broadcast over WebSockets so dashboards refresh immediately.
//
// Each step is best-effort — a failure in one doesn't roll back the
// others. The webhook always returns 200 so Lens doesn't retry.
func (h *WebhookHandler) handleSpendAlert(ctx context.Context, p SpendAlertPayload, eventKey string) error {
	if p.WorkspaceID == "" || p.Feature == "" {
		return errors.New("spend_alert: workspace_id and feature required")
	}

	// Idempotent cost accumulation: RecordSpendEvent writes one ai_spend_events row per
	// credited issue and accumulates atomically, keyed by eventKey — a re-delivered
	// event adds nothing. Limitation: two genuinely-distinct events with byte-identical
	// bodies collapse to one — errs toward UNDER-count, the safe direction for a cost
	// number; the durable fix is a Lens-sent event_id.
	if _, err := h.issues.RecordSpendEvent(ctx, eventKey, p.Feature, p.CostUSD, 0, p.WorkspaceID, "webhook"); err != nil {
		slog.Warn("spend_alert: RecordSpendEvent failed",
			slog.String("feature", p.Feature),
			slog.String("err", err.Error()),
		)
	}

	// Look up the issue by its identifier (Lens uses the issue
	// identifier as the X-Talyvor-Feature value). If no match,
	// notification simply has no assignee — that's still useful.
	issue, _ := h.issues.GetByIdentifier(ctx, p.Feature)

	if h.notifications != nil && issue != nil && issue.AssigneeID != nil {
		_, err := h.notifications.Create(ctx, notification.Notification{
			WorkspaceID: p.WorkspaceID,
			MemberID:    *issue.AssigneeID,
			Type:        "spend_alert",
			Title:       fmt.Sprintf("AI spend alert: %s", p.Feature),
			Body: fmt.Sprintf(
				"%s has used $%.2f in AI tokens (threshold: $%.2f)",
				p.Feature, p.CostUSD, p.Threshold,
			),
			IssueID: &issue.ID,
		})
		if err != nil {
			slog.Warn("spend_alert: create notification failed",
				slog.String("feature", p.Feature),
				slog.String("err", err.Error()),
			)
		}
	}

	// Real-time fanout so anyone viewing the issue sees the alert
	// without refreshing. ActorID empty — the actor is Lens itself.
	if h.notifier != nil && issue != nil {
		h.notifier.IssueUpdated(ctx, p.WorkspaceID, issue.TeamID, issue.ID, "", map[string]any{
			"ai_cost_usd":   p.CostUSD,
			"spend_alert":   true,
			"threshold_usd": p.Threshold,
			"alert_feature": p.Feature,
		})
	}
	return nil
}

// verifySignature constant-time-compares the expected HMAC-SHA256
// against the provided header value. Lens sends the signature as a
// hex string in the X-Lens-Signature header.
func verifySignature(body []byte, providedHex, secret string) bool {
	if providedHex == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(providedHex)) == 1
}

// signForTest produces the hex HMAC a Lens server would send. Tests
// use this to construct valid signed requests. Not part of the public
// API in production code — tests and the Lens implementation re-derive
// the signature on their own side.
func signForTest(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Error: msg, Code: code})
}
