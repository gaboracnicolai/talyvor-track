package lensintegration

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"net/http/httptest"

	"github.com/talyvor/track/internal/model"
)

// W3.5 — THE WEBHOOK USES ONE STRING AS TWO DIFFERENT KEYS, FOUR LINES APART.
//
// handleSpendAlert passes p.Feature to both of these:
//
//	h.issues.RecordSpendEvent(…, p.Feature, …)  ->  issue.Store: WHERE lens_feature = $2
//	h.issues.GetByIdentifier(ctx, p.Feature, …) ->  issue.Store: WHERE identifier   = $1
//
// `issues` has BOTH columns and they are different fields: `identifier` is ENG-42 and
// `lens_feature` is the Lens feature tag (migrations 0002/0006; settable through the issue
// update allowlist). So for the ordinary case — an issue whose lens_feature is the tag the
// editor sends — THE COST LANDS AND THE ALERT REACHES NOBODY, and nothing anywhere says so.
//
// WHAT THESE TESTS DO AND DO NOT DECIDE. They do NOT change which column is queried. Which
// key is right is a product call: an operator who configures an alert rule whose feature IS
// an issue identifier gets a working notification today and would lose it. What is NOT a
// product call is that the divergence is SILENT — RecordSpendEvent already returns the number
// of issues it credited and handleSpendAlert throws it away, so the one moment where the
// handler can PROVE the two keys disagreed is discarded. These tests pin that it must be loud.

// divergentLookup models the real store: two independent columns, and a lookup that can fail.
type divergentLookup struct {
	mu sync.Mutex

	identifier  string // the row GetByIdentifier will match
	lensFeature string // the row RecordSpendEvent will credit
	issue       *model.Issue
	lookupErr   error

	credited  int // what RecordSpendEvent reports back
	costCalls int
}

func (d *divergentLookup) GetByIdentifier(_ context.Context, ident, _ string) (*model.Issue, error) {
	if d.lookupErr != nil {
		return nil, d.lookupErr
	}
	if d.issue != nil && ident == d.identifier {
		return d.issue, nil
	}
	return nil, nil
}

func (d *divergentLookup) RecordSpendEvent(_ context.Context, _, feature string, _ float64, _ int, _, _ string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.costCalls++
	if feature == d.lensFeature {
		return d.credited, nil
	}
	return 0, nil
}

// captureLogs swaps the default slog handler for one that keeps every record, and restores it.
// The handler is what the production path already writes to, so this asserts the shipped
// observability rather than a test-only hook.
func captureLogs(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	prev := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sink
}

type logSink struct {
	mu      sync.Mutex
	records []slog.Record
}

func (s *logSink) Enabled(context.Context, slog.Level) bool { return true }
func (s *logSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r.Clone())
	return nil
}
func (s *logSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *logSink) WithGroup(string) slog.Handler      { return s }

// matching returns the records at or above WARN whose message contains sub.
func (s *logSink) matching(sub string) []slog.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []slog.Record
	for _, r := range s.records {
		if r.Level >= slog.LevelWarn && strings.Contains(r.Message, sub) {
			out = append(out, r)
		}
	}
	return out
}

const divergeSecret = "w35-divergence-secret"

func divergePost(t *testing.T, wh *WebhookHandler, feature string) {
	t.Helper()
	body := []byte(`{"type":"spend_alert","workspace_id":"ws-1","feature":"` + feature + `","cost_usd":4.20,"threshold":1.00}`)
	rec := httptest.NewRecorder()
	wh.ServeHTTP(rec, signedRequest(t, divergeSecret, body))
	if rec.Code != 200 {
		t.Fatalf("webhook status = %d, want 200", rec.Code)
	}
}

// RED (a): the money lands by lens_feature and NOTHING matches as an identifier. Today the
// handler is completely silent — no notification, no fanout, and no log line. A spend alert
// that reaches nobody is indistinguishable, from outside, from one that never fired.
func TestSpendAlert_CreditedButNoIdentifierMatch_IsLoud(t *testing.T) {
	sink := captureLogs(t)
	issues := &divergentLookup{
		identifier:  "ENG-1",     // the issue's key in the tracker
		lensFeature: "code-chat", // …and the tag the editor actually sends
		issue:       &model.Issue{ID: "iss-1", Identifier: "ENG-1", WorkspaceID: "ws-1"},
		credited:    1,
	}
	notes := &recordingNotifications{}
	notif := &recordingNotifier{}
	wh := NewWebhookHandler(divergeSecret, issues, notes, notif)

	divergePost(t, wh, "code-chat")

	// The behaviour itself is NOT changed by this test — it is pinned so the decision about
	// which column is right is taken against a measured baseline rather than a memory.
	if len(notes.created) != 0 {
		t.Fatalf("notifications created = %d, want 0 — this is the baseline this item is about", len(notes.created))
	}
	if notif.updates != 0 {
		t.Fatalf("realtime fanouts = %d, want 0 — the fanout is gated on the SAME lookup", notif.updates)
	}

	got := sink.matching("credited")
	if len(got) != 1 {
		t.Fatalf("WARN records naming the credit/notify divergence = %d, want exactly 1.\n"+
			"RecordSpendEvent credited 1 issue by lens_feature and GetByIdentifier matched nothing, "+
			"so the handler KNOWS the two keys disagreed and currently says nothing at all.", len(got))
	}
}

// CONTROL (b): the ordinary working case must stay quiet. Without this the guard above could
// be satisfied by a handler that warns on every alert, which would be noise, not a signal.
func TestSpendAlert_IdentifierMatches_StaysQuiet(t *testing.T) {
	sink := captureLogs(t)
	assignee := "mem-1"
	issues := &divergentLookup{
		identifier:  "ENG-1",
		lensFeature: "ENG-1", // the operator who configured the rule with an issue key
		issue:       &model.Issue{ID: "iss-1", Identifier: "ENG-1", WorkspaceID: "ws-1", AssigneeID: &assignee},
		credited:    1,
	}
	notes := &recordingNotifications{}
	wh := NewWebhookHandler(divergeSecret, issues, notes, &recordingNotifier{})

	divergePost(t, wh, "ENG-1")

	if len(notes.created) != 1 {
		t.Fatalf("notifications created = %d, want 1 — this path works today and must keep working", len(notes.created))
	}
	if got := sink.matching("credited"); len(got) != 0 {
		t.Fatalf("WARN records = %d, want 0 — a handler that warns on the WORKING case is noise:\n  %v", len(got), got)
	}
}

// CONTROL (c): nothing was credited AND nothing matched. That is an alert for a feature this
// workspace does not track at all — uninteresting, and it must not be reported as a divergence,
// or every stray alert becomes a false alarm about attribution.
func TestSpendAlert_NothingCreditedAndNoMatch_IsNotADivergence(t *testing.T) {
	sink := captureLogs(t)
	issues := &divergentLookup{identifier: "ENG-1", lensFeature: "ENG-1", credited: 0}
	wh := NewWebhookHandler(divergeSecret, issues, &recordingNotifications{}, &recordingNotifier{})

	divergePost(t, wh, "something-nobody-tracks")

	if got := sink.matching("credited"); len(got) != 0 {
		t.Fatalf("WARN records = %d, want 0 — nothing was credited, so nothing diverged:\n  %v", len(got), got)
	}
}

// RED (d): the lookup's error is discarded (`issue, _ :=`). A Postgres failure and a genuine
// no-match produce byte-identical behaviour — no notification, no fanout, no word — so an
// outage on this path is invisible. The two must be distinguishable.
func TestSpendAlert_LookupError_IsReportedSeparately(t *testing.T) {
	sink := captureLogs(t)
	issues := &divergentLookup{
		identifier:  "ENG-1",
		lensFeature: "ENG-1",
		lookupErr:   errors.New("connection refused"),
		credited:    1,
	}
	wh := NewWebhookHandler(divergeSecret, issues, &recordingNotifications{}, &recordingNotifier{})

	divergePost(t, wh, "ENG-1")

	got := sink.matching("issue lookup failed")
	if len(got) != 1 {
		t.Fatalf("WARN records naming a FAILED lookup = %d, want exactly 1 — a database error "+
			"currently looks exactly like 'no such issue'", len(got))
	}
	// …and it must NOT be reported as the attribution divergence, which is a different fact
	// with a different fix.
	if d := sink.matching("credited"); len(d) != 0 {
		t.Fatalf("a failed lookup was ALSO reported as a key divergence (%d records) — it is not "+
			"one: nothing is known about whether an identifier would have matched", len(d))
	}
}
