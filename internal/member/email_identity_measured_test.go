package member_test

// email_identity_measured_test.go — THE `members` TABLE IS THE ONLY EMAIL-KEYED IDENTITY IN THIS
// REPOSITORY THAT DOES NOT CANONICALISE THE ADDRESS, AND IT IS THE ONE THAT DECIDES AUTHZ.
//
// ⚠⚠ NOTHING IS FIXED HERE AND THAT IS DELIBERATE. Canonicalising `members.email` changes WHO CAN
// AUTHENTICATE and needs a backfill over rows that already exist — lowercasing on write WITHOUT
// migrating the existing mixed-case rows makes exactly the people in them permanently unreachable,
// which is the lockout this file measures. Which form is canonical, and what happens to rows that
// collide once it is applied, is a decision. See the queue item this file was written for.
// These tests PIN TODAY'S BEHAVIOUR so it cannot change silently and so nobody re-derives it.
//
// ⚠ IF ONE OF THESE GOES RED, READ THIS BEFORE "FIXING" IT. A red here most likely means the
// canonicalisation LANDED, which is the fix and not a regression. Update these tests to the new
// rule; do not loosen them.
//
// ── THE RULE EXISTS IN THIS CODEBASE. `members` IS THE EXCEPTION. ──────────────────────────────
//
//	internal/guest/store.go:259        strings.ToLower(strings.TrimSpace(email))   ✓ and rejects blank
//	internal/featureboard/store.go     strings.ToLower(strings.TrimSpace(email))   ✓ three call sites
//	internal/member/mgmt.go#AddMember  the raw string, straight into the INSERT     ✗
//
// All three columns are plain `TEXT` with a UNIQUE that includes email
// (`UNIQUE(workspace_id,email)` on members and guests, `UNIQUE(post_id,email)` on feature_votes),
// so in Postgres the constraint is CASE-SENSITIVE and only the two normalising ports make it mean
// "one per person". This is not a codebase that has not thought about email canonicalisation: it
// decided the rule, applied it twice, and left it out of the one place where getting it wrong ends
// in a 403 for a legitimate owner.
//
// ── AND THE TWO PRODUCERS OF `members.email` DISAGREE BY CONSTRUCTION ─────────────────────────
//
//	workspace/handler.go:64 + bootstrap.go:131   CreateWithOwner(..., id.Email)   ← the GATEWAY
//	                                              identity: whatever the IdP sends, machine-cased
//	member/mgmt_handler.go:107                    AddMember(..., in.Email)        ← a JSON body
//	                                              field an admin TYPED
//
// mgmt_handler.go:97 rejects `in.Email == ""` and nothing else — the obvious empty value guarded,
// and the value a human actually sends left alone.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/guest"
	"github.com/talyvor/track/internal/member"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/workspace"
)

func newAcmeWorkspace(t *testing.T, d *testutil.DB, slug, ownerEmail string) *model.Workspace {
	t.Helper()
	ws, err := workspace.NewStore(d.Pool).CreateWithOwner(context.Background(),
		model.Workspace{Name: "Acme", Slug: slug}, ownerEmail)
	if err != nil {
		t.Fatalf("CreateWithOwner: %v", err)
	}
	return ws
}

// TestMeasured_OneHumanHoldsManyMembershipsInOneWorkspace pins the mechanism. AddMember's own doc
// comment says "A UNIQUE(workspace_id, email) collision returns ErrMemberExists" — true only for
// BYTE-IDENTICAL addresses, which is not what "member exists" means to the admin reading it.
func TestMeasured_OneHumanHoldsManyMembershipsInOneWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := newAcmeWorkspace(t, d, "acme-many", "owner@acme.com")
	ms := member.NewStore(d.Pool)

	// Five spellings of one person, in the shapes a human or a paste actually produces.
	spellings := []string{
		"alice@acme.com", "Alice@Acme.com", "ALICE@ACME.COM",
		" alice@acme.com", "alice@acme.com ",
	}
	for _, e := range spellings {
		if _, err := ms.AddMember(ctx, ws.ID, e, "member"); err != nil {
			t.Fatalf("AddMember(%q) = %v.\nIf this is ErrMemberExists the canonicalisation has "+
				"LANDED — that is the fix; update this file rather than loosening it.", e, err)
		}
	}

	var n int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM members WHERE workspace_id=$1`, ws.ID).Scan(&n); err != nil {
		t.Fatalf("count members: %v", err)
	}
	// 5 spellings + the owner CreateWithOwner seeded.
	if n != 6 {
		t.Errorf("workspace holds %d member rows, measured 6 (one owner + five spellings of one "+
			"person). UNIQUE(workspace_id,email) refused none of them.", n)
	}

	// And the resolver keys on byte equality, so a spelling nobody stored is simply not a member.
	r := authz.NewPGResolver(d.Pool)
	for _, e := range []string{"alice@acme.com", "Alice@Acme.com"} {
		mm, err := r.MembershipsByEmail(ctx, e)
		if err != nil || len(mm) != 1 {
			t.Errorf("MembershipsByEmail(%q) = %d, %v; want exactly 1 — each spelling is its own "+
				"membership", e, len(mm), err)
		}
	}
	mm, _ := r.MembershipsByEmail(ctx, "alice@ACME.com")
	if len(mm) != 0 {
		t.Errorf("MembershipsByEmail(%q) = %d; measured 0. A spelling that was never stored is not "+
			"a member, however many times this person is.", "alice@ACME.com", len(mm))
	}
}

// TestMeasured_TheLastOwnerGuardCanLeaveNoReachableOwner is the consequence, and it is the reason
// this file exists rather than a note in a comment.
//
// member/mgmt.go documents RemoveMember as refusing to remove the LAST owner — "lockout hazard b" —
// and takes the count under a row lock so two concurrent removals cannot race the workspace to
// zero. The count is over owner ROWS. A row whose email no IdP will ever send is an owner nobody
// can authenticate as, and it satisfies that count exactly as well as a real one.
//
// MEASURED end to end through the real stores, no mocks:
//
//	Alice creates the workspace  → owner row `alice@acme.com`   (from the GATEWAY identity)
//	Alice invites Bob as owner   → owner row `Bob@Acme.com`     (TYPED into the JSON body)
//	Bob signs in as bob@acme.com → 0 memberships → HTTP 403     (an owner who cannot get in)
//	owner rows = 2 → Alice may be removed
//	→ ONE owner row remains AND NO HUMAN CAN AUTHENTICATE AS IT.
func TestMeasured_TheLastOwnerGuardCanLeaveNoReachableOwner(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := newAcmeWorkspace(t, d, "acme-lockout", "alice@acme.com")
	ms := member.NewStore(d.Pool)
	r := authz.NewPGResolver(d.Pool)

	if _, err := ms.AddMember(ctx, ws.ID, "Bob@Acme.com", "owner"); err != nil {
		t.Fatalf("inviting a co-owner with a typed address failed: %v", err)
	}

	// PREMISE, asserted rather than assumed: Bob genuinely cannot authenticate. Without this the
	// removal below would just be an ordinary owner handover.
	if mm, err := r.MembershipsByEmail(ctx, "bob@acme.com"); err != nil || len(mm) != 0 {
		t.Fatalf("premise: bob@acme.com resolves to %d membership(s) (err %v); measured 0. If it "+
			"resolves now, emails are being canonicalised and this test must be rewritten, not "+
			"relaxed.", len(mm), err)
	}

	var aliceID string
	if err := d.Pool.QueryRow(ctx,
		`SELECT id FROM members WHERE workspace_id=$1 AND email='alice@acme.com'`,
		ws.ID).Scan(&aliceID); err != nil {
		t.Fatalf("find alice: %v", err)
	}

	// THE ASSERTION. Today this SUCCEEDS. It is pinned as a measurement of the hazard, not as
	// desired behaviour.
	err := ms.RemoveMember(ctx, ws.ID, aliceID)
	if err != nil {
		t.Fatalf("RemoveMember(alice) = %v.\nMeasured behaviour is that it SUCCEEDS: the "+
			"last-owner guard counts owner ROWS and `Bob@Acme.com` is one. A refusal here means "+
			"the guard learned to count REACHABLE owners — that is the fix; update this file.", err)
	}

	// The state the guard permitted: one owner row, nobody who can sign into it.
	var owners int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM members WHERE workspace_id=$1 AND role='owner'`, ws.ID).Scan(&owners); err != nil {
		t.Fatalf("count owners: %v", err)
	}
	if owners != 1 {
		t.Fatalf("owner rows = %d, want 1 — this test is measuring the wrong state", owners)
	}
	for _, e := range []string{"alice@acme.com", "bob@acme.com"} {
		mm, err := r.MembershipsByEmail(ctx, e)
		if err != nil {
			t.Fatalf("resolve %q: %v", e, err)
		}
		if len(mm) != 0 {
			t.Errorf("%q still resolves to %d membership(s); measured 0. The point of this test is "+
				"that the workspace has an owner ROW and no reachable owner.", e, len(mm))
		}
	}
	// …and the row that remains is reachable only by the exact string an admin happened to type.
	mm, _ := r.MembershipsByEmail(ctx, "Bob@Acme.com")
	if len(mm) != 1 || mm[0].Role != authz.RoleOwner {
		t.Errorf("the surviving owner row resolves to %+v for the typed spelling; want exactly one "+
			"owner membership", mm)
	}
}

// TestMeasured_GuestsCanonicaliseAndMembersDoNot makes the divergence EXECUTABLE rather than a
// grep. The same address, typed the same way, into the two stores that both key identity on email
// and both carry UNIQUE(workspace_id, email) — one lowercases and trims it, the other does not.
func TestMeasured_GuestsCanonicaliseAndMembersDoNot(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := newAcmeWorkspace(t, d, "acme-divergence", "owner@acme.com")

	const typed = "  Carol@Acme.Com  "

	if _, err := member.NewStore(d.Pool).AddMember(ctx, ws.ID, typed, "member"); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	var stored string
	if err := d.Pool.QueryRow(ctx,
		`SELECT email FROM members WHERE workspace_id=$1 AND role='member'`, ws.ID).Scan(&stored); err != nil {
		t.Fatalf("read member email: %v", err)
	}
	if stored != typed {
		t.Errorf("members stored %q for input %q — it now normalises. That is the fix landing; "+
			"update this file rather than relaxing it.", stored, typed)
	}

	gs := guest.NewStore(d.Pool, "test-guest-secret-that-is-long-enough-32b")
	if _, err := gs.CreateInvite(ctx, ws.ID, nil, typed, guest.GuestRoleViewer, "owner@acme.com"); err != nil {
		t.Fatalf("guest CreateInvite: %v", err)
	}
	var guestStored string
	if err := d.Pool.QueryRow(ctx,
		`SELECT email FROM guest_invites WHERE workspace_id=$1`, ws.ID).Scan(&guestStored); err != nil {
		t.Fatalf("read guest email: %v", err)
	}
	if guestStored != "carol@acme.com" {
		t.Errorf("guest_invites stored %q for input %q, want %q — guest/store.go:259 applies "+
			"strings.ToLower(strings.TrimSpace(email)). If THIS changed, the repo's own precedent "+
			"for the canonical form has moved and the members question changes with it.",
			guestStored, typed, "carol@acme.com")
	}

	if stored == guestStored {
		t.Errorf("both stores now agree on %q — the divergence this file measures is gone, which "+
			"is the outcome the queue item asks for. Rewrite this test to pin the new rule.", stored)
	}
}

// TestMeasured_TheGatewayProducerStoresTheRawAddressToo closes a hole this file's own controls
// found. PC5 of the merge review canonicalised ONLY `workspace.CreateWithOwner` — the GATEWAY
// producer named in this file's header — and all three tests above stayed GREEN, because every
// owner address they seed is already canonical. So the file's headline claim ("members is the only
// email-keyed identity that is not canonicalised") could have become half true with its whole
// guard green, and the next session would have read three passing tests as covering both writers.
//
// `CreateWithOwner` interpolates `ownerEmail` twice, raw, into `INSERT INTO members (…name, email…)`
// (workspace/store.go:151). That is the IdP-supplied identity, so in practice it arrives
// machine-cased and this is latent rather than firing — which is exactly why nothing else pins it.
func TestMeasured_TheGatewayProducerStoresTheRawAddressToo(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()

	const fromIdP = "  Dave@Acme.Com  "
	ws := newAcmeWorkspace(t, d, "acme-gateway", fromIdP)

	var stored string
	if err := d.Pool.QueryRow(ctx,
		`SELECT email FROM members WHERE workspace_id=$1 AND role='owner'`, ws.ID).Scan(&stored); err != nil {
		t.Fatalf("read owner email: %v", err)
	}
	if stored != fromIdP {
		t.Errorf("CreateWithOwner stored %q for owner email %q — the GATEWAY producer now "+
			"normalises. That is half the fix landing; the other half is member.AddMember, which "+
			"the tests above pin. Update both rather than relaxing either.", stored, fromIdP)
	}

	// And the consequence, so this is a measurement of ACCESS and not of a string: the owner row
	// exists and the address the IdP would send next time reaches nothing.
	r := authz.NewPGResolver(d.Pool)
	if mm, err := r.MembershipsByEmail(ctx, "dave@acme.com"); err != nil || len(mm) != 0 {
		t.Errorf("MembershipsByEmail(%q) = %d, %v; measured 0 — the seeded owner is reachable "+
			"only by the exact bytes the gateway sent.", "dave@acme.com", len(mm), err)
	}
	if mm, err := r.MembershipsByEmail(ctx, fromIdP); err != nil || len(mm) != 1 {
		t.Errorf("MembershipsByEmail(%q) = %d, %v; want exactly 1 — this is the spelling that was "+
			"stored, and it is the only one that resolves.", fromIdP, len(mm), err)
	}
}
