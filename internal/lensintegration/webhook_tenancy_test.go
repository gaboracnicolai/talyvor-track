package lensintegration

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// AUDIT SWEEP (unscoped identifier lookup, Lens side): the GitHub webhook was the write
// path, but handleSpendAlert resolved the same way — GetByIdentifier(p.Feature) with no
// tenancy filter. Identifiers are unique per workspace only, so a spend alert naming
// workspace A could resolve workspace B's identically-identified issue and then
//
//   - create a notification addressed to B's assignee, and
//   - broadcast the alert into B's realtime issue room, carrying A's dollar figure.
//
// That is a cross-tenant read plus a cross-tenant notification, not merely a mis-join.
// The fixtures in webhook_test.go never set WorkspaceID at all, which is precisely why
// the old lookup's missing filter went unnoticed there.

// RED before the fix: the ws-2 issue is found by identifier and both side effects fire.
// GREEN after: the alert names ws-1, the issue lives in ws-2, nothing is addressed to it.
func TestWebhook_SpendAlert_DoesNotReachAnotherTenantsIssue(t *testing.T) {
	assignee := "bob-in-ws-2"
	issues := &recordingIssueLookup{
		issue: &model.Issue{
			ID:          "issue-in-ws-2",
			WorkspaceID: "ws-2", // a DIFFERENT tenant than the alert names
			TeamID:      "team-2",
			Identifier:  "ENG-42", // …that happens to share the identifier
			AssigneeID:  &assignee,
		},
	}
	notifs := &recordingNotifications{}
	notif := &recordingNotifier{}
	wh := NewWebhookHandler("s", issues, notifs, notif)

	body, _ := json.Marshal(SpendAlertPayload{
		Type: "spend_alert", WorkspaceID: "ws-1", Feature: "ENG-42",
		CostUSD: 12.50, Threshold: 10.00,
	})
	wh.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "s", body))

	if len(notifs.created) != 0 {
		t.Fatalf("CROSS-TENANT NOTIFICATION: %d notification(s) created for a ws-2 issue from a ws-1 alert (first routed to %q)",
			len(notifs.created), notifs.created[0].MemberID)
	}
	if notif.updates != 0 {
		t.Fatalf("CROSS-TENANT BROADCAST: %d realtime update(s) pushed into ws-2's issue room from a ws-1 alert (payload=%+v)",
			notif.updates, notif.last)
	}
}
