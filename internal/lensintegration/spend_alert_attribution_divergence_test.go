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

// W3.5 → B18.33 — A SPEND ALERT REACHES THE PEOPLE WHOSE ISSUES WERE CHARGED.
//
// handleSpendAlert credits every issue whose lens_feature is the alert's feature (RecordSpendEvent)
// and notifies those same issues' assignees (ListByLensFeature). Only when no issue carries that
// lens_feature does it fall back to the issue whose IDENTIFIER is the feature, so an operator who
// keyed an alert rule on an issue identifier keeps the notification they had. A miss sends nothing
// and says nothing; a failed lookup is warned about.
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

func (d *divergentLookup) ListByLensFeature(_ context.Context, feature, _ string) ([]*model.Issue, error) {
	if d.lookupErr != nil {
		return nil, d.lookupErr
	}
	if d.issue != nil && feature == d.lensFeature {
		return []*model.Issue{d.issue}, nil
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

// The ordinary case: the issue is tagged with the feature the editor sends ("code-chat"), its
// identifier is ENG-1. The alert reaches that issue's assignee and its realtime room, quietly.
func TestSpendAlert_NotifiesTheIssueThatWasCharged(t *testing.T) {
	sink := captureLogs(t)
	assignee := "mem-1"
	issues := &divergentLookup{
		identifier:  "ENG-1",
		lensFeature: "code-chat",
		issue:       &model.Issue{ID: "iss-1", Identifier: "ENG-1", WorkspaceID: "ws-1", AssigneeID: &assignee},
		credited:    1,
	}
	notes := &recordingNotifications{}
	notif := &recordingNotifier{}
	wh := NewWebhookHandler(divergeSecret, issues, notes, notif)

	divergePost(t, wh, "code-chat")

	if len(notes.created) != 1 || notes.created[0].MemberID != assignee || *notes.created[0].IssueID != "iss-1" {
		t.Fatalf("notifications = %+v, want one for the charged issue's assignee", notes.created)
	}
	if notif.updates != 1 {
		t.Fatalf("realtime fanouts = %d, want 1 for the charged issue", notif.updates)
	}
	if w := sink.matching(""); len(w) != 0 {
		t.Fatalf("WARN records = %d, want 0 on the working path", len(w))
	}
}

// An operator who keyed the alert rule on an issue IDENTIFIER, with no issue tagged by that
// lens_feature, keeps the notification they had.
func TestSpendAlert_IdentifierRuleStillNotifies(t *testing.T) {
	assignee := "mem-1"
	issues := &divergentLookup{
		identifier:  "ENG-1",
		lensFeature: "code-chat",
		issue:       &model.Issue{ID: "iss-1", Identifier: "ENG-1", WorkspaceID: "ws-1", AssigneeID: &assignee},
	}
	notes := &recordingNotifications{}
	wh := NewWebhookHandler(divergeSecret, issues, notes, &recordingNotifier{})

	divergePost(t, wh, "ENG-1")

	if len(notes.created) != 1 {
		t.Fatalf("notifications created = %d, want 1 — the identifier-keyed rule must keep working", len(notes.created))
	}
}

// An alert for a feature this workspace does not track reaches nobody and warns about nothing.
func TestSpendAlert_NoMatch_SendsNothingQuietly(t *testing.T) {
	sink := captureLogs(t)
	issues := &divergentLookup{identifier: "ENG-1", lensFeature: "code-chat"}
	notes := &recordingNotifications{}
	wh := NewWebhookHandler(divergeSecret, issues, notes, &recordingNotifier{})

	divergePost(t, wh, "something-nobody-tracks")

	if len(notes.created) != 0 {
		t.Fatalf("notifications created = %d, want 0", len(notes.created))
	}
	if w := sink.matching(""); len(w) != 0 {
		t.Fatalf("WARN records = %d, want 0 — an untracked feature is not an error", len(w))
	}
}

// A database error is not "no such issue": it is warned about, and nothing is sent.
func TestSpendAlert_LookupError_IsReported(t *testing.T) {
	sink := captureLogs(t)
	issues := &divergentLookup{
		identifier:  "ENG-1",
		lensFeature: "code-chat",
		lookupErr:   errors.New("connection refused"),
		credited:    1,
	}
	notes := &recordingNotifications{}
	wh := NewWebhookHandler(divergeSecret, issues, notes, &recordingNotifier{})

	divergePost(t, wh, "code-chat")

	if got := sink.matching("issue lookup failed"); len(got) != 1 {
		t.Fatalf("WARN records naming a failed lookup = %d, want exactly 1", len(got))
	}
	if len(notes.created) != 0 {
		t.Fatalf("notifications created = %d after a failed lookup, want 0", len(notes.created))
	}
}
