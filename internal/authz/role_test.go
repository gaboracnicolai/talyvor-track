package authz

import (
	"context"
	"testing"
)

// Role/IsOwner are the member-tier gate. Fail-closed is the whole point: an empty
// or unrecognised role is NOT owner, and a request that never passed the {wsID}
// boundary has no role at all.
func TestRoleGate_FailClosed(t *testing.T) {
	// No auth context at all → no role, not owner.
	if _, ok := Role(context.Background()); ok {
		t.Fatal("bare ctx must carry no role")
	}
	if IsOwner(context.Background()) {
		t.Fatal("bare ctx must NOT be owner (fail-closed)")
	}

	// Authorized as a plain member.
	memCtx := WithAuthorizedRole(context.Background(), "ws1", "m1", RoleMember)
	if r, ok := Role(memCtx); !ok || r != RoleMember {
		t.Fatalf("Role(member) = %q,%v want %q,true", r, ok, RoleMember)
	}
	if IsOwner(memCtx) {
		t.Fatal("a member must not be treated as owner")
	}

	// Authorized as owner.
	ownCtx := WithAuthorizedRole(context.Background(), "ws1", "m1", RoleOwner)
	if !IsOwner(ownCtx) {
		t.Fatal("an owner ctx must be owner")
	}

	// Unknown/garbage role → fail-closed, not owner.
	if IsOwner(WithAuthorizedRole(context.Background(), "ws1", "m1", "superadmin")) {
		t.Fatal("an unrecognised role must NOT be owner (fail-closed)")
	}
	if IsOwner(WithAuthorizedRole(context.Background(), "ws1", "m1", "")) {
		t.Fatal("an empty role must NOT be owner (fail-closed)")
	}

	// IsOwnerRole (used by flat routes that hold a Membership, e.g. integrations).
	if !IsOwnerRole(RoleOwner) {
		t.Fatal("IsOwnerRole(owner) must be true")
	}
	for _, r := range []string{RoleMember, "", "admin", "OWNER"} {
		if IsOwnerRole(r) {
			t.Fatalf("IsOwnerRole(%q) must be false (exact, fail-closed)", r)
		}
	}
}
