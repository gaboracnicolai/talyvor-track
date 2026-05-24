package guest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func newMockStore(t *testing.T) (*Store, pgxmock.PgxPoolIface) {
	t.Helper()
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	return newStore(pool, "test-secret-32-bytes-or-whatever"), pool
}

func inviteRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "project_id", "email", "role",
		"token", "expires_at", "accepted_at", "invited_by", "created_at",
	})
}

func guestRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "workspace_id", "project_id", "email", "name", "role",
		"active", "created_at", "last_seen_at",
	})
}

func ptr[T any](v T) *T { return &v }

// ─── CreateInvite ───────────────────────────────────────────

func TestCreateInvite_GeneratesUniqueToken(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// The store generates the token + expiry server-side; we accept
	// any args here so the test isn't tied to the random bytes the
	// implementation picks.
	pool.ExpectQuery(`INSERT INTO guest_invites`).
		WithArgs("ws-1", (*string)(nil), "alice@example.com", "viewer",
			pgxmock.AnyArg(), pgxmock.AnyArg(), "member-1").
		WillReturnRows(inviteRows().AddRow(
			"inv-1", "ws-1", (*string)(nil), "alice@example.com", "viewer",
			"tok-aaaa", now.Add(7*24*time.Hour), (*time.Time)(nil), "member-1", now,
		))

	out, err := store.CreateInvite(context.Background(), "ws-1", nil,
		"alice@example.com", GuestRoleViewer, "member-1")
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if out.Token == "" {
		t.Error("Token should be non-empty")
	}
	if out.ExpiresAt.Before(now) {
		t.Error("ExpiresAt should be in the future")
	}
}

func TestCreateInvite_RejectsInvalidRole(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.CreateInvite(context.Background(), "ws-1", nil,
		"a@b.com", GuestRole("haxxor"), "member-1")
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
}

func TestCreateInvite_RejectsEmptyEmail(t *testing.T) {
	store, _ := newMockStore(t)
	_, err := store.CreateInvite(context.Background(), "ws-1", nil,
		"", GuestRoleViewer, "member-1")
	if err == nil {
		t.Fatal("expected error for empty email")
	}
}

// ─── AcceptInvite ───────────────────────────────────────────

func TestAcceptInvite_CreatesGuestAndMarksInviteAccepted(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// Lookup unaccepted invite by token.
	pool.ExpectQuery(`SELECT .* FROM guest_invites WHERE token`).
		WithArgs("tok-abc").
		WillReturnRows(inviteRows().AddRow(
			"inv-1", "ws-1", (*string)(nil), "alice@example.com", "viewer",
			"tok-abc", now.Add(7*24*time.Hour), (*time.Time)(nil), "member-1", now,
		))
	// Insert (or upsert) guest record.
	pool.ExpectQuery(`INSERT INTO guests`).
		WithArgs("ws-1", (*string)(nil), "alice@example.com", "Alice", "viewer").
		WillReturnRows(guestRows().AddRow(
			"g-1", "ws-1", (*string)(nil), "alice@example.com", "Alice", "viewer",
			true, now, (*time.Time)(nil),
		))
	// Mark invite accepted.
	pool.ExpectExec(`UPDATE guest_invites SET accepted_at`).
		WithArgs("inv-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	out, err := store.AcceptInvite(context.Background(), "tok-abc", "Alice")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if out.guest.ID != "g-1" || out.guest.Name != "Alice" {
		t.Errorf("got %+v", out.guest)
	}
	if out.accessToken == "" {
		t.Error("access_token should be non-empty")
	}
	// Verify the signed token decodes to the right guest.
	claims, err := store.VerifyToken(out.accessToken)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.GuestID != "g-1" || claims.WorkspaceID != "ws-1" || claims.Role != GuestRoleViewer {
		t.Errorf("claims = %+v", claims)
	}
}

func TestAcceptInvite_RejectsExpiredInvite(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`SELECT .* FROM guest_invites WHERE token`).
		WithArgs("tok-old").
		WillReturnRows(inviteRows().AddRow(
			"inv-2", "ws-1", (*string)(nil), "bob@example.com", "viewer",
			"tok-old", now.Add(-time.Hour), (*time.Time)(nil), "member-1", now.Add(-8*24*time.Hour),
		))

	_, err := store.AcceptInvite(context.Background(), "tok-old", "Bob")
	if err == nil {
		t.Fatal("expected error for expired invite")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention expiry; got %v", err)
	}
}

func TestAcceptInvite_RejectsAlreadyAcceptedInvite(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	accepted := now.Add(-time.Hour)
	pool.ExpectQuery(`SELECT .* FROM guest_invites WHERE token`).
		WithArgs("tok-used").
		WillReturnRows(inviteRows().AddRow(
			"inv-3", "ws-1", (*string)(nil), "c@example.com", "viewer",
			"tok-used", now.Add(7*24*time.Hour), &accepted, "member-1", now,
		))

	_, err := store.AcceptInvite(context.Background(), "tok-used", "C")
	if err == nil {
		t.Fatal("expected error for already-accepted invite")
	}
}

// ─── ListGuests ─────────────────────────────────────────────

func TestListGuests_ReturnsWorkspaceGuests(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	pool.ExpectQuery(`FROM guests WHERE workspace_id`).
		WithArgs("ws-1").
		WillReturnRows(guestRows().
			AddRow("g-1", "ws-1", (*string)(nil), "alice@example.com", "Alice", "viewer",
				true, now, (*time.Time)(nil)).
			AddRow("g-2", "ws-1", ptr("p-1"), "bob@example.com", "Bob", "editor",
				true, now, (*time.Time)(nil)))

	out, err := store.ListGuests(context.Background(), "ws-1", nil)
	if err != nil {
		t.Fatalf("ListGuests: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
}

func TestListGuests_FiltersByProject(t *testing.T) {
	store, pool := newMockStore(t)
	now := time.Now().UTC()
	// project filter returns workspace-wide guests + project-scoped
	// guests so a client can render "all guests on this project page".
	pool.ExpectQuery(`project_id IS NULL OR project_id = \$2`).
		WithArgs("ws-1", "p-1").
		WillReturnRows(guestRows().
			AddRow("g-1", "ws-1", (*string)(nil), "alice@example.com", "Alice", "viewer",
				true, now, (*time.Time)(nil)))

	out, err := store.ListGuests(context.Background(), "ws-1", ptr("p-1"))
	if err != nil {
		t.Fatalf("ListGuests: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("got %d, want 1", len(out))
	}
}

// ─── RevokeGuest ────────────────────────────────────────────

func TestRevokeGuest_DeactivatesGuest(t *testing.T) {
	store, pool := newMockStore(t)
	pool.ExpectExec(`UPDATE guests SET active = false`).
		WithArgs("g-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := store.RevokeGuest(context.Background(), "g-1"); err != nil {
		t.Fatalf("RevokeGuest: %v", err)
	}
}

// ─── ValidateGuestAccess ────────────────────────────────────

func TestValidateGuestAccess_WorkspaceScope(t *testing.T) {
	store, _ := newMockStore(t)
	// A workspace-wide guest gets access to workspace + any project.
	tok := store.signClaims(&GuestClaims{
		GuestID:     "g-1",
		WorkspaceID: "ws-1",
		Role:        GuestRoleViewer,
		ExpiresUnix: time.Now().Add(time.Hour).Unix(),
	})
	role, err := store.ValidateGuestAccess(context.Background(), tok, "workspace", "ws-1")
	if err != nil {
		t.Fatalf("ValidateGuestAccess workspace: %v", err)
	}
	if role != GuestRoleViewer {
		t.Errorf("role = %q, want viewer", role)
	}
	if _, err := store.ValidateGuestAccess(context.Background(), tok, "workspace", "other-ws"); err == nil {
		t.Error("expected error for wrong workspace")
	}
}

func TestValidateGuestAccess_ProjectScope(t *testing.T) {
	store, _ := newMockStore(t)
	tok := store.signClaims(&GuestClaims{
		GuestID:     "g-1",
		WorkspaceID: "ws-1",
		ProjectID:   "p-1",
		Role:        GuestRoleEditor,
		ExpiresUnix: time.Now().Add(time.Hour).Unix(),
	})
	role, err := store.ValidateGuestAccess(context.Background(), tok, "project", "p-1")
	if err != nil {
		t.Fatalf("ValidateGuestAccess project: %v", err)
	}
	if role != GuestRoleEditor {
		t.Errorf("role = %q, want editor", role)
	}
	if _, err := store.ValidateGuestAccess(context.Background(), tok, "project", "p-2"); err == nil {
		t.Error("expected error for wrong project")
	}
}

func TestValidateGuestAccess_ExpiredTokenRejected(t *testing.T) {
	store, _ := newMockStore(t)
	tok := store.signClaims(&GuestClaims{
		GuestID:     "g-1",
		WorkspaceID: "ws-1",
		Role:        GuestRoleViewer,
		ExpiresUnix: time.Now().Add(-time.Minute).Unix(),
	})
	if _, err := store.ValidateGuestAccess(context.Background(), tok, "workspace", "ws-1"); err == nil {
		t.Error("expected error for expired token")
	}
}

func TestValidateGuestAccess_TamperedTokenRejected(t *testing.T) {
	store, _ := newMockStore(t)
	tok := store.signClaims(&GuestClaims{
		GuestID:     "g-1",
		WorkspaceID: "ws-1",
		Role:        GuestRoleViewer,
		ExpiresUnix: time.Now().Add(time.Hour).Unix(),
	})
	// Flip a character in the signature half.
	parts := strings.Split(tok, ".")
	tampered := parts[0] + ".XXX" + parts[1][3:]
	if _, err := store.ValidateGuestAccess(context.Background(), tampered, "workspace", "ws-1"); err == nil {
		t.Error("expected error for tampered token")
	}
}
