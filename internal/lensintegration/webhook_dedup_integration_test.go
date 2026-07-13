package lensintegration

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/webhookdedup"
)

// SEC-7 REMAINDER — durable Lens event_id dedup + freshness window (real PG).
// The dedup reuses #49's webhookdedup.Store (source="lens"); the body-hash dedup
// is kept as the fallback. Bodies are RAW JSON so the tests exercise wire
// behaviour (old Track ignores unknown fields), not the Go struct.

const dedupSecret = "sec7-secret"

func freshTS() string                  { return time.Now().UTC().Format(time.RFC3339) }
func staleTS(age time.Duration) string { return time.Now().Add(-age).UTC().Format(time.RFC3339) }

// (a) RED: two alerts with the SAME event_id but DIFFERENT bytes. The body-hash
// dedup can't catch this (bytes differ); only a durable event_id dedup can.
// Today: both processed (2 RecordSpendEvent calls). After: the second is a no-op.
func TestWebhook_EventIDDedup_ByteVariedReplay_Integration(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	issues := &recordingIssueLookup{}
	wh := NewWebhookHandler(dedupSecret, issues, &recordingNotifications{}, &recordingNotifier{}).
		WithDeduper(webhookdedup.New(d.Pool)).
		WithFreshness(5 * time.Minute)

	// Same event_id "evt-a"; DIFFERENT cost_usd bytes → body-hash differs.
	body1 := []byte(`{"type":"spend_alert","workspace_id":"ws-1","feature":"ENG-1","cost_usd":1.00,"threshold":0.5,"event_id":"evt-a","emitted_at":"` + freshTS() + `"}`)
	body2 := []byte(`{"type":"spend_alert","workspace_id":"ws-1","feature":"ENG-1","cost_usd":2.00,"threshold":0.5,"event_id":"evt-a","emitted_at":"` + freshTS() + `"}`)

	wh.ServeHTTP(httptest.NewRecorder(), signedRequest(t, dedupSecret, body1))
	wh.ServeHTTP(httptest.NewRecorder(), signedRequest(t, dedupSecret, body2))

	if issues.costCalls != 1 {
		t.Fatalf("RecordSpendEvent calls = %d, want 1 — a byte-varied replay with the SAME event_id must be deduped (the body-hash can't catch it)", issues.costCalls)
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE source='lens' AND delivery_id='evt-a'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("webhook_deliveries rows for (lens, evt-a) = %d, want 1 (claimed exactly once)", n)
	}
}

// (b) RED: an alert whose emitted_at is older than the freshness window. Today:
// processed. After: rejected (no side effects), still 200 so Lens doesn't retry.
func TestWebhook_FreshnessWindow_RejectsStale_Integration(t *testing.T) {
	d := testutil.New(t)
	issues := &recordingIssueLookup{}
	wh := NewWebhookHandler(dedupSecret, issues, &recordingNotifications{}, &recordingNotifier{}).
		WithDeduper(webhookdedup.New(d.Pool)).
		WithFreshness(5 * time.Minute)

	// emitted_at 10m ago, window 5m → stale.
	body := []byte(`{"type":"spend_alert","workspace_id":"ws-1","feature":"ENG-1","cost_usd":1.0,"threshold":0.5,"event_id":"evt-stale","emitted_at":"` + staleTS(10*time.Minute) + `"}`)
	w := httptest.NewRecorder()
	wh.ServeHTTP(w, signedRequest(t, dedupSecret, body))

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (a rejected alert is still acknowledged)", w.Code)
	}
	if issues.costCalls != 0 {
		t.Fatalf("RecordSpendEvent calls = %d, want 0 — an alert older than the freshness window must be rejected", issues.costCalls)
	}
}

// (c) REGRESSION GUARD: exact re-delivery with NO event_id must STILL be
// suppressed by the body-hash fallback. Passes before AND after the fix (proves
// the fallback survives). Uses the REAL issue store so ai_spend_events' ON
// CONFLICT (event_key,…) actually fires.
func TestWebhook_BodyHashFallback_ExactRedelivery_Integration(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	feat := "ENG-FALLBACK"
	if _, err := issue.NewStore(d.Pool).Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, Title: "fallback", CreatorID: "c", LensFeature: feat,
	}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	wh := NewWebhookHandler(dedupSecret, issue.NewStore(d.Pool), &recordingNotifications{}, &recordingNotifier{}).
		WithDeduper(webhookdedup.New(d.Pool)).
		WithFreshness(5 * time.Minute)

	// No event_id / emitted_at (today's reality). Exact same bytes twice.
	body := []byte(fmt.Sprintf(`{"type":"spend_alert","workspace_id":"%s","feature":"%s","cost_usd":3.0,"threshold":1.0}`, ws.ID, feat))
	wh.ServeHTTP(httptest.NewRecorder(), signedRequest(t, dedupSecret, body))
	wh.ServeHTTP(httptest.NewRecorder(), signedRequest(t, dedupSecret, body))

	var rows int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM ai_spend_events WHERE lens_feature=$1`, feat).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("ai_spend_events rows = %d, want 1 — the exact re-delivery must be suppressed by the body-hash fallback", rows)
	}
}
