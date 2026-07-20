package notification_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/notification"
	"github.com/talyvor/track/internal/testutil"
)

func notifListReq(wsID, memberID string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+wsID+"/notifications", nil)
	return r.WithContext(authz.WithAuthorizedRole(r.Context(), wsID, memberID, authz.RoleMember))
}

func seedMemberID(t *testing.T, d *testutil.DB, wsID, email string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'member') RETURNING id`,
		wsID, email, email).Scan(&id); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return id
}

func seedNotif(t *testing.T, d *testutil.DB, wsID, memberID, title string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(),
		`INSERT INTO notifications (workspace_id, member_id, type, title) VALUES ($1,$2,'mention',$3)`,
		wsID, memberID, title); err != nil {
		t.Fatalf("seed notif: %v", err)
	}
}

// DROP-VERIFICATION: the brief lists notification list as unscoped, but the read keys on
// authz.MemberID — a SERVER-derived id, and members.id is unique per (workspace, email). So the
// same person is a DIFFERENT member id in each workspace, and List(memberID) is already single-
// workspace: a member of A and B, reading under {wsID}=A (=> memberID = their A member id), sees
// only A's notifications. This test must PASS on current code (no leak) — confirming the item is
// already scoped and needs no change. If it ever fails, the premise broke and it becomes a real fix.
func TestNotification_List_AlreadyWorkspaceScopedByMemberID(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	// Same person, member of BOTH workspaces → two distinct member ids.
	mA := seedMemberID(t, d, wsA.ID, "alice@x.com")
	mB := seedMemberID(t, d, wsB.ID, "alice@x.com")
	seedNotif(t, d, wsA.ID, mA, "A-notif")
	seedNotif(t, d, wsB.ID, mB, "B-notif")
	h := notification.NewHandler(notification.NewStore(d.Pool))

	// Reading under {wsID}=A (memberID resolved to mA) → only A's notification, never B's.
	rrA := httptest.NewRecorder()
	h.List(rrA, notifListReq(wsA.ID, mA))
	if !strings.Contains(rrA.Body.String(), "A-notif") {
		t.Errorf("reading under wsA should show A's notification; got %s", rrA.Body.String())
	}
	if strings.Contains(rrA.Body.String(), "B-notif") {
		t.Fatalf("MERGED-VIEW LEAK: reading under wsA showed wsB's notification: %s", rrA.Body.String())
	}

	// Symmetric: reading under {wsID}=B (memberID mB) → only B's.
	rrB := httptest.NewRecorder()
	h.List(rrB, notifListReq(wsB.ID, mB))
	if strings.Contains(rrB.Body.String(), "A-notif") {
		t.Fatalf("MERGED-VIEW LEAK: reading under wsB showed wsA's notification: %s", rrB.Body.String())
	}
}
