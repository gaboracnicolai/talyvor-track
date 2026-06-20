package lensintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/notification"
)

type recordingIssueLookup struct {
	mu       sync.Mutex
	costCall struct {
		Feature   string
		Cost      float64
		Workspace string
	}
	costCalls int
	issue     *model.Issue
}

func (r *recordingIssueLookup) GetByIdentifier(_ context.Context, ident string) (*model.Issue, error) {
	if r.issue != nil && r.issue.Identifier == ident {
		return r.issue, nil
	}
	return nil, nil
}
func (r *recordingIssueLookup) RecordSpendEvent(_ context.Context, _, feature string, cost float64, _ int, ws, _ string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.costCalls++
	r.costCall.Feature = feature
	r.costCall.Cost = cost
	r.costCall.Workspace = ws
	return 1, nil
}

type recordingNotifications struct {
	mu      sync.Mutex
	created []notification.Notification
}

func (r *recordingNotifications) Create(_ context.Context, n notification.Notification) (*notification.Notification, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = append(r.created, n)
	return &n, nil
}

type recordingNotifier struct {
	mu      sync.Mutex
	updates int
	last    map[string]any
}

func (r *recordingNotifier) IssueUpdated(_ context.Context, _, _, _, _ string, changes map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates++
	r.last = changes
}

func signedRequest(t *testing.T, secret string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/lens/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lens-Signature", signForTest(body, secret))
	return req
}

func TestWebhook_ValidSignatureAccepted(t *testing.T) {
	body := []byte(`{"type":"spend_alert","workspace_id":"ws-1","feature":"ENG-1","cost_usd":1.0,"threshold":0.5}`)
	issues := &recordingIssueLookup{}
	wh := NewWebhookHandler("topsecret", issues, &recordingNotifications{}, &recordingNotifier{})

	req := signedRequest(t, "topsecret", body)
	w := httptest.NewRecorder()
	wh.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestWebhook_InvalidSignatureReturns401(t *testing.T) {
	body := []byte(`{"type":"spend_alert","workspace_id":"ws-1","feature":"ENG-1"}`)
	wh := NewWebhookHandler("topsecret", &recordingIssueLookup{}, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/lens/webhook", bytes.NewReader(body))
	req.Header.Set("X-Lens-Signature", "totally-wrong")
	w := httptest.NewRecorder()
	wh.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestWebhook_EmptySecretRefusesAllRequests(t *testing.T) {
	body := []byte(`{"type":"spend_alert"}`)
	wh := NewWebhookHandler("", &recordingIssueLookup{}, nil, nil)

	req := signedRequest(t, "", body)
	w := httptest.NewRecorder()
	wh.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when secret unset", w.Code)
	}
}

func TestWebhook_SpendAlertUpdatesIssueAICost(t *testing.T) {
	assignee := "alice"
	issues := &recordingIssueLookup{
		issue: &model.Issue{
			ID:         "issue-1",
			TeamID:     "team-1",
			Identifier: "ENG-42",
			AssigneeID: &assignee,
		},
	}
	wh := NewWebhookHandler("s", issues, &recordingNotifications{}, &recordingNotifier{})

	body, _ := json.Marshal(SpendAlertPayload{
		Type: "spend_alert", WorkspaceID: "ws-1", Feature: "ENG-42",
		CostUSD: 12.50, Threshold: 10.00,
	})
	req := signedRequest(t, "s", body)
	w := httptest.NewRecorder()
	wh.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if issues.costCalls != 1 {
		t.Errorf("UpdateAICost calls = %d, want 1", issues.costCalls)
	}
	if issues.costCall.Feature != "ENG-42" || issues.costCall.Cost != 12.50 {
		t.Errorf("UpdateAICost wrong args: %+v", issues.costCall)
	}
}

func TestWebhook_SpendAlertCreatesNotificationForAssignee(t *testing.T) {
	assignee := "alice"
	issues := &recordingIssueLookup{
		issue: &model.Issue{
			ID: "issue-1", TeamID: "team-1",
			Identifier: "ENG-42", AssigneeID: &assignee,
		},
	}
	notifs := &recordingNotifications{}
	wh := NewWebhookHandler("s", issues, notifs, &recordingNotifier{})

	body, _ := json.Marshal(SpendAlertPayload{
		Type: "spend_alert", WorkspaceID: "ws-1", Feature: "ENG-42",
		CostUSD: 12.50, Threshold: 10.00,
	})
	wh.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "s", body))

	if len(notifs.created) != 1 {
		t.Fatalf("got %d notifications, want 1", len(notifs.created))
	}
	n := notifs.created[0]
	if n.MemberID != "alice" {
		t.Errorf("notification routed to %q, want alice (the assignee)", n.MemberID)
	}
	if n.Type != "spend_alert" {
		t.Errorf("notification type = %q, want spend_alert", n.Type)
	}
}

func TestWebhook_SpendAlertBroadcastsToRealtime(t *testing.T) {
	assignee := "alice"
	issues := &recordingIssueLookup{
		issue: &model.Issue{
			ID: "issue-1", TeamID: "team-1",
			Identifier: "ENG-42", AssigneeID: &assignee,
		},
	}
	notif := &recordingNotifier{}
	wh := NewWebhookHandler("s", issues, &recordingNotifications{}, notif)

	body, _ := json.Marshal(SpendAlertPayload{
		Type: "spend_alert", WorkspaceID: "ws-1", Feature: "ENG-42",
		CostUSD: 12.50, Threshold: 10.00,
	})
	wh.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "s", body))

	if notif.updates != 1 {
		t.Errorf("IssueUpdated calls = %d, want 1", notif.updates)
	}
	if notif.last["spend_alert"] != true {
		t.Errorf("broadcast payload missing spend_alert flag: %+v", notif.last)
	}
}
