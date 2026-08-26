package notification_test

// crossworkspace_ref_realpg_test.go — THE CROSS-OBJECT REF GUARD IN THIS PACKAGE WAS HELD BY
// NOTHING. MEASURED at 713d22a by making the guard INERT (the call left in place, its refusal
// disabled: `if err := tenancy.AssertRefInWorkspace(...); err != nil && false {`) and asking the
// WHOLE repository with real Postgres. Five of twelve such sites scored NOT CAUGHT — notification,
// label, project, guest and customfield — and this is one of them.
//
// ⚠ SEMGREP CANNOT HOLD ANY OF THE TWELVE. .semgrep/cross-object-tenancy.yml exempts a function
// that CALLS a ref guard; that asserts the CALL IS PRESENT and nothing about its ANSWER, so a
// guard whose refusal never fires keeps the exemption. Scored 0 of 12 CAUGHT. #174 recorded the
// identical limit for rule C and it was never re-measured on these arms.
//
// ⚠ THE COMPANION IS NOT DECORATION. "Create refuses a foreign ref" is satisfied completely by a
// Create that refuses that ref for EVERYONE, which is a different bug wearing the same red. Each
// case therefore also asserts the workspace’s OWN ref is accepted.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/notification"
	"github.com/talyvor/track/internal/testutil"
)

func seedMember(t *testing.T, d *testutil.DB, wsID, email string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO members (workspace_id, name, email) VALUES ($1, 'M', $2) RETURNING id`,
		wsID, email).Scan(&id); err != nil {
		t.Fatalf("seed member %s: %v", email, err)
	}
	return id
}

// TestNotification_CrossWorkspaceMemberIsRefused_RealPG — a notification may not be addressed to a
// member of another workspace.
func TestNotification_CrossWorkspaceMemberIsRefused_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	mine, theirs := d.Workspace(t), d.Workspace(t)
	local := seedMember(t, d, mine.ID, "mine@x.com")
	foreign := seedMember(t, d, theirs.ID, "theirs@x.com")
	s := notification.NewStore(d.Pool)

	if got, err := s.Create(ctx, notification.Notification{
		WorkspaceID: mine.ID, MemberID: foreign, Type: "mention", Title: "cross-tenant",
	}); err == nil {
		t.Errorf("Create ACCEPTED a member from another workspace (%s) — the ref guard is not "+
			"refusing. Row created as %v.", foreign, got.ID)
	}

	// MUST STAY GREEN: the workspace's own member is accepted.
	if _, err := s.Create(ctx, notification.Notification{
		WorkspaceID: mine.ID, MemberID: local, Type: "mention", Title: "same-workspace",
	}); err != nil {
		t.Fatalf("Create REFUSED the workspace's own member (%s): %v — the refusal above is not a "+
			"tenancy check, it is a blanket rejection.", local, err)
	}
}
