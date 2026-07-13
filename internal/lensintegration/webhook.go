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
	"time"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/notification"
)

// notifier is the slice of internal/realtime.Notifier the webhook
// needs. Local interface keeps the package boundary clean.
type notifier interface {
	IssueUpdated(ctx context.Context, wsID, teamID, issueID, actorID string, changes map[string]any)
}

// deliveryDeduper claims a delivery id exactly once (SEC-7). Claim returns true
// the FIRST time a (source, deliveryID) is seen and false on any repeat. Mirrors
// the GitHub handler's guard; injected as an interface so this package stays free
// of a DB import. A nil deduper preserves pre-SEC-7 behaviour (event_id dedup
// skipped — the body-hash fallback still guards). *webhookdedup.Store satisfies it.
type deliveryDeduper interface {
	Claim(ctx context.Context, source, deliveryID string) (claimed bool, err error)
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

	// SEC-7 durable replay guard. Both optional so the handler is unchanged when
	// unset (the body-hash dedup below still protects the path today).
	deduper   deliveryDeduper // event_id dedup (reuse #49's webhookdedup.Store); nil = skip
	freshness time.Duration   // reject alerts whose emitted_at is older than this; 0 = disabled
}

func NewWebhookHandler(secret string, issues issueLookup, notifications notificationCreator, notif notifier) *WebhookHandler {
	return &WebhookHandler{
		secret:        secret,
		issues:        issues,
		notifications: notifications,
		notifier:      notif,
	}
}

// WithDeduper wires the durable event_id replay guard (SEC-7). Optional —
// nil leaves the body-hash dedup as the sole guard (pre-emitter Lens). Mirrors
// the GitHub handler's WithDeduper.
func (h *WebhookHandler) WithDeduper(d deliveryDeduper) *WebhookHandler {
	h.deduper = d
	return h
}

// WithFreshness sets the max age for a spend alert's emitted_at (SEC-7). An alert
// older than this is rejected. 0 (default) disables the check. Config-driven
// (TRACK_LENS_WEBHOOK_FRESHNESS, default 5m).
func (h *WebhookHandler) WithFreshness(d time.Duration) *WebhookHandler {
	h.freshness = d
	return h
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

	// SEC-7 durable replay guard. OPTIONAL until the Lens emitter sends them; a
	// missing value is accepted (the handler falls back to the body-hash dedup and
	// logs), so the rollout is safe in either order.
	//   EventID   — server-generated UUID, unique per emitted alert. Deduped via
	//               webhookdedup.Store (source="lens"): catches byte-varied replays
	//               the body-hash misses.
	//   EmittedAt — RFC3339 UTC server clock. Rejected if older than the freshness
	//               window (stops an ancient captured alert replayed after the
	//               dedup key was pruned; the timestamp is signed, so unforgeable).
	EventID   string `json:"event_id,omitempty"`
	EmittedAt string `json:"emitted_at,omitempty"`
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
		// SEC-7 FRESHNESS: reject an alert whose signed emitted_at is older than the
		// window. A replayed capture carries its ORIGINAL emitted_at (any change
		// breaks the HMAC), so this stops an ancient alert re-POSTed after its dedup
		// key was pruned. Missing/unparseable emitted_at (an old Lens) → ACCEPT + log,
		// so the rollout stays order-independent. `break` here exits the type switch,
		// falling through to the 200 with NO side effects.
		if h.freshness > 0 && payload.EmittedAt != "" {
			if ts, perr := time.Parse(time.RFC3339, payload.EmittedAt); perr != nil {
				slog.Warn("lensintegration: spend alert emitted_at unparseable — accepting (freshness skipped)",
					slog.String("emitted_at", payload.EmittedAt), slog.String("feature", payload.Feature))
			} else if age := time.Since(ts); age > h.freshness {
				slog.Warn("lensintegration: spend alert REJECTED — stale emitted_at",
					slog.Duration("age", age), slog.Duration("window", h.freshness),
					slog.String("workspace_id", payload.WorkspaceID), slog.String("feature", payload.Feature))
				break
			}
		} else if payload.EmittedAt == "" {
			slog.Info("lensintegration: spend alert has no emitted_at — freshness skipped (pre-emitter Lens)",
				slog.String("feature", payload.Feature))
		}

		// SEC-7 DURABLE DEDUP: claim the server-generated event_id exactly once,
		// reusing #49's webhookdedup.Store (source="lens"). Catches byte-varied
		// replays the body-hash misses. Missing event_id or nil deduper → fall back
		// to the body-hash dedup below (log). A claim ERROR → proceed (the body-hash
		// still guards) rather than drop a real alert. `break` on a repeat exits the
		// type switch → 200, no side effects. NOTE: if/else-if (not switch) so break
		// targets the outer type switch.
		if payload.EventID != "" && h.deduper != nil {
			claimed, cerr := h.deduper.Claim(r.Context(), "lens", payload.EventID)
			if cerr != nil {
				slog.Warn("lensintegration: event_id claim failed — proceeding (body-hash still guards)",
					slog.String("event_id", payload.EventID), slog.String("err", cerr.Error()))
			} else if !claimed {
				slog.Info("lensintegration: spend alert DEDUPED on event_id (durable replay guard)",
					slog.String("event_id", payload.EventID), slog.String("feature", payload.Feature))
				break
			}
		} else if payload.EventID == "" {
			slog.Info("lensintegration: spend alert has no event_id — body-hash fallback only (pre-emitter Lens)",
				slog.String("feature", payload.Feature))
		}

		// Body-hash dedup (the KEPT fallback): idempotency key = hash of the exact
		// signed body. An identical re-delivery maps to the same key and is recorded
		// exactly once — this is what protects the path while no Lens sends event_id.
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
	// event adds nothing. This body-hash key is now the FALLBACK: SEC-7's durable
	// event_id dedup (ServeHTTP, above) sits in front and catches byte-varied replays
	// this can't. The residual byte-identical-distinct-events collapse errs toward
	// UNDER-count (safe for a cost number) and only applies to alerts with no event_id.
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
