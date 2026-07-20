package member_test

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/migrations"
)

// The 0024 backfill enforces "every workspace has >=1 owner". On clean data it is a no-op;
// on a zero-owner workspace it REFUSES (raises) rather than guessing a promotion. The DO
// block is a pure check with no writes, so re-running it against the migrated DB in two
// states is a faithful exercise of the invariant.
func TestBackfill0024_RefusesZeroOwnerWorkspace(t *testing.T) {
	d := testutil.New(t) // applies all migrations incl. 0024 (no-op on the empty DB)
	ctx := context.Background()

	sqlBytes, err := migrations.FS.ReadFile("0024_members_owner_backfill.sql")
	if err != nil {
		t.Fatalf("read 0024: %v", err)
	}
	backfill := string(sqlBytes)

	// Positive: a workspace WITH an owner → re-running the backfill is a clean no-op.
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "owner@x.com", authz.RoleOwner)
	if _, err := d.Pool.Exec(ctx, backfill); err != nil {
		t.Fatalf("backfill must pass when every workspace has an owner, got: %v", err)
	}

	// Negative: add a workspace whose only member is a non-owner → backfill must REFUSE.
	orphan := d.Workspace(t)
	seedMember(t, d, orphan.ID, "member@x.com", authz.RoleMember)
	_, err = d.Pool.Exec(ctx, backfill)
	if err == nil {
		t.Fatal("backfill MUST refuse a zero-owner workspace, but it succeeded (would silently allow a lockout)")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "zero owner") {
		t.Fatalf("backfill refusal should name the invariant; got: %v", err)
	}
}
