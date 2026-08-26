package lensintegration

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/notification"
	"github.com/talyvor/track/internal/testutil"
)

// W3.5, MEASURED AGAINST THE REAL SCHEMA RATHER THAN A MOCK.
//
// The mock-based tests next door can only show that the handler calls two functions. This one
// shows that the two functions read two DIFFERENT COLUMNS of the same table, by putting a row
// in each and watching the money and the alert go to different people.
//
// This test asserts TODAY'S behaviour on purpose. It is a characterisation test, not a red:
// the decision about which column is right is a product call (see the comment in webhook.go),
// and a decision is taken better against a pinned baseline than against a memory. If someone
// changes the key, this test fails and its message says exactly what moved.

const twoKeysSecret = "w35-two-keys-secret"

// seedIssueWithFeature creates an issue with an explicit identifier, lens_feature and assignee,
// and returns (issueID, memberID). Identifier and lens_feature are forced with UPDATE because
// Create derives the identifier itself and never writes lens_feature (it is omitted from the
// insert as a money-path column).
func seedIssueWithFeature(t *testing.T, d *testutil.DB, wsID, teamID, identifier, lensFeature, email string) (string, string) {
	t.Helper()
	ctx := context.Background()
	var memberID string
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO members (workspace_id, name, email) VALUES ($1,$2,$2) RETURNING id`,
		wsID, email).Scan(&memberID); err != nil {
		t.Fatalf("member %s: %v", email, err)
	}
	st := issue.NewStore(d.Pool)
	out, err := st.Create(ctx, model.Issue{
		WorkspaceID: wsID, TeamID: teamID, Title: identifier, CreatorID: memberID,
		Identifier: identifier, AssigneeID: &memberID,
	})
	if err != nil {
		t.Fatalf("create %s: %v", identifier, err)
	}
	if _, err := d.Pool.Exec(ctx,
		`UPDATE issues SET identifier=$1, lens_feature=$2, assignee_id=$3 WHERE id=$4`,
		identifier, lensFeature, memberID, out.ID); err != nil {
		t.Fatalf("force keys on %s: %v", identifier, err)
	}
	return out.ID, memberID
}

func TestSpendAlert_MoneyFollowsLensFeature_AlertFollowsIdentifier_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsID := "ws-w35"

	if _, err := d.Pool.Exec(ctx, `INSERT INTO workspaces (id,name,slug) VALUES ($1,$1,$1)`, wsID); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	var teamID string
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO teams (workspace_id,name,identifier) VALUES ($1,'Eng','ENG') RETURNING id`,
		wsID).Scan(&teamID); err != nil {
		t.Fatalf("team: %v", err)
	}

	// THE ISSUE THE ENGINEER IS ACTUALLY WORKING ON: keyed ENG-1 in the tracker, tagged
	// `code-chat` for Lens — which is the value the Code extension Talyvor ships sends.
	money, moneyAssignee := seedIssueWithFeature(t, d, wsID, teamID, "ENG-1", "code-chat", "money@example.com")
	// A DIFFERENT issue that merely happens to be KEYED `code-chat`.
	alert, alertAssignee := seedIssueWithFeature(t, d, wsID, teamID, "code-chat", "", "alert@example.com")

	issues := issue.NewStore(d.Pool)
	notes := notification.NewStore(d.Pool)
	wh := NewWebhookHandler(twoKeysSecret, issues, notes, nil)

	body := []byte(`{"type":"spend_alert","workspace_id":"` + wsID + `","feature":"code-chat","cost_usd":7.50,"threshold":1.00}`)
	rec := httptest.NewRecorder()
	wh.ServeHTTP(rec, signedRequest(t, twoKeysSecret, body))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// THE MONEY: credited to the issue whose lens_feature matched (ENG-1), not the one keyed
	// `code-chat`. Asserted on the rollup AND the ledger, never on a return value.
	var moneyCost, alertCost float64
	if err := d.Pool.QueryRow(ctx, `SELECT ai_cost_usd FROM issues WHERE id=$1`, money).Scan(&moneyCost); err != nil {
		t.Fatalf("rollup ENG-1: %v", err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT ai_cost_usd FROM issues WHERE id=$1`, alert).Scan(&alertCost); err != nil {
		t.Fatalf("rollup code-chat: %v", err)
	}
	if moneyCost != 7.50 {
		t.Fatalf("ENG-1 (lens_feature=code-chat) ai_cost_usd = %v, want 7.50 — the credit matches lens_feature", moneyCost)
	}
	if alertCost != 0 {
		t.Fatalf("the issue KEYED code-chat has ai_cost_usd = %v, want 0 — the credit does not match identifier", alertCost)
	}

	// THE ALERT: sent to the assignee of the issue whose IDENTIFIER matched — a different person.
	var gotMember, gotIssue string
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if n != 1 {
		t.Fatalf("notifications = %d, want 1", n)
	}
	if err := d.Pool.QueryRow(ctx,
		`SELECT member_id, COALESCE(issue_id,'') FROM notifications WHERE workspace_id=$1`,
		wsID).Scan(&gotMember, &gotIssue); err != nil {
		t.Fatalf("read notification: %v", err)
	}

	if gotMember != alertAssignee || gotIssue != alert {
		t.Fatalf("notification went to member %s / issue %s; expected the IDENTIFIER match "+
			"(member %s / issue %s)", gotMember, gotIssue, alertAssignee, alert)
	}
	if gotMember == moneyAssignee {
		t.Fatalf("the notification reached the assignee of the CREDITED issue — the two keys have " +
			"been reconciled, and this test is the record of what they used to do. Update it and W3.5.")
	}
	t.Logf("MEASURED: $7.50 credited to %s (identifier ENG-1, lens_feature code-chat); "+
		"the alert notified %s, the assignee of a DIFFERENT issue (identifier code-chat). "+
		"One string, two columns, one function.", moneyAssignee, alertAssignee)
}
