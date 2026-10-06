package member_test

// email_identity_test.go — B18.32: a member's email matches regardless of case and surrounding
// spaces, and the address is kept exactly as it was typed. authz resolves one membership per
// workspace; member.AddMember refuses a second spelling of an address the workspace already has.
// Rows that already collide are resolved, never merged (scripts/member-email-collisions.sh lists them).

import (
	"context"
	"errors"
	"testing"

	"github.com/talyvor/track/internal/authz"
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

// "Ann@X.com" and "ann@x.com" sign in as the same member, and the stored address is untouched.
func TestMemberEmail_AnyCaseSignsInAsTheSameMember(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := newAcmeWorkspace(t, d, "acme-case", "Ann@X.com")
	r := authz.NewPGResolver(d.Pool)

	var memberID string
	for _, e := range []string{"Ann@X.com", "ann@x.com", "  ANN@x.COM "} {
		mm, err := r.MembershipsByEmail(ctx, e)
		if err != nil || len(mm) != 1 || mm[0].WorkspaceID != ws.ID || mm[0].Role != authz.RoleOwner {
			t.Fatalf("MembershipsByEmail(%q) = %+v, %v; want the one owner membership", e, mm, err)
		}
		if memberID == "" {
			memberID = mm[0].MemberID
		} else if mm[0].MemberID != memberID {
			t.Errorf("%q resolved to member %s, want %s — the same person", e, mm[0].MemberID, memberID)
		}
	}
	var stored string
	if err := d.Pool.QueryRow(ctx, `SELECT email FROM members WHERE id=$1`, memberID).Scan(&stored); err != nil || stored != "Ann@X.com" {
		t.Errorf("stored email = %q (%v), want it kept as typed: %q", stored, err, "Ann@X.com")
	}
}

// Adding a second spelling of an address the workspace already has is refused.
func TestMemberEmail_AddMemberRefusesASecondSpelling(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := newAcmeWorkspace(t, d, "acme-add", "owner@acme.com")
	ms := member.NewStore(d.Pool)

	if _, err := ms.AddMember(ctx, ws.ID, "", "Bob@Acme.com", "member"); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := ms.AddMember(ctx, ws.ID, "", " bob@acme.com", "owner"); !errors.Is(err, member.ErrMemberExists) {
		t.Errorf("AddMember of a second spelling = %v, want ErrMemberExists", err)
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM members WHERE workspace_id=$1`, ws.ID).Scan(&n); err != nil || n != 2 {
		t.Errorf("workspace holds %d member rows (%v), want 2: the owner and Bob once", n, err)
	}
}

// Two rows that already collide are resolved to one membership — the exact spelling when it exists,
// otherwise the owner row — and neither is deleted.
func TestMemberEmail_ExistingCollisionResolvesToOneMembership(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := newAcmeWorkspace(t, d, "acme-collide", "owner@acme.com")
	var memberRow, ownerRow string
	if err := d.Pool.QueryRow(ctx, `INSERT INTO members (workspace_id, name, email, role)
		VALUES ($1, 'c', 'carol@acme.com', 'member') RETURNING id`, ws.ID).Scan(&memberRow); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool.QueryRow(ctx, `INSERT INTO members (workspace_id, name, email, role)
		VALUES ($1, 'C', 'Carol@Acme.com', 'owner') RETURNING id`, ws.ID).Scan(&ownerRow); err != nil {
		t.Fatal(err)
	}

	r := authz.NewPGResolver(d.Pool)
	for email, want := range map[string]string{
		"carol@acme.com": memberRow, // exact spelling wins
		"Carol@Acme.com": ownerRow,  // exact spelling wins
		"CAROL@acme.com": ownerRow,  // no exact spelling: the owner row
	} {
		mm, err := r.MembershipsByEmail(ctx, email)
		if err != nil || len(mm) != 1 || mm[0].MemberID != want {
			t.Errorf("MembershipsByEmail(%q) = %+v, %v; want exactly member %s", email, mm, err, want)
		}
	}
	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM members WHERE workspace_id=$1`, ws.ID).Scan(&n); err != nil || n != 3 {
		t.Errorf("member rows = %d (%v), want 3 — nothing is merged automatically", n, err)
	}
}
