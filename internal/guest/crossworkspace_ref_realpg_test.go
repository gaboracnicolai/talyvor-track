package guest

// crossworkspace_ref_realpg_test.go — CreateInvite's project ref guard was held by nothing.
//
// MEASURED at 713d22a by making the guard INERT (the call left in place, its refusal disabled:
// `if err := tenancy.AssertRefInWorkspace(...); err != nil && false {`) and asking the WHOLE
// repository with real Postgres. Five of twelve such sites scored NOT CAUGHT — notification,
// label, project, guest and customfield — and this is one of them.
//
// ⚠ SEMGREP CANNOT HOLD ANY OF THE TWELVE. .semgrep/cross-object-tenancy.yml exempts a function
// that CALLS a ref guard; that asserts the CALL IS PRESENT and nothing about its ANSWER, so a
// guard whose refusal never fires keeps the exemption. Scored 0 of 12 CAUGHT.
//
// ⚠ THE COMPANION IS NOT DECORATION. "CreateInvite refuses a foreign project" is satisfied
// completely by one that refuses every project, which is a different bug wearing the same red.

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

func seedProjectIn(t *testing.T, d *testutil.DB, wsID, teamID, ident string) string {
	t.Helper()
	p, err := project.NewStore(d.Pool).Create(context.Background(), model.Project{
		WorkspaceID: wsID, TeamID: teamID, Name: "P " + ident, Identifier: ident,
	})
	if err != nil {
		t.Fatalf("seed project %s: %v", ident, err)
	}
	return p.ID
}

// TestCreateInvite_CrossWorkspaceProjectIsRefused_RealPG — a guest invite may not be scoped to a
// project in another workspace.
func TestCreateInvite_CrossWorkspaceProjectIsRefused_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	mine, theirs := d.Workspace(t), d.Workspace(t)
	myTeam, theirTeam := d.Team(t, mine.ID), d.Team(t, theirs.ID)
	local := seedProjectIn(t, d, mine.ID, myTeam.ID, "MINE")
	foreign := seedProjectIn(t, d, theirs.ID, theirTeam.ID, "THRS")
	s := NewStore(d.Pool, testGuestSecret)

	if got, err := s.CreateInvite(ctx, mine.ID, &foreign, "a@x.com", GuestRoleViewer, "inviter"); err == nil {
		t.Errorf("CreateInvite ACCEPTED a project from another workspace (%s) — the ref guard is "+
			"not refusing. Invite created as %v.", foreign, got.ID)
	}

	// MUST STAY GREEN: the workspace's own project is accepted.
	if _, err := s.CreateInvite(ctx, mine.ID, &local, "b@x.com", GuestRoleViewer, "inviter"); err != nil {
		t.Fatalf("CreateInvite REFUSED the workspace's own project (%s): %v — the refusal above is "+
			"not a tenancy check, it is a blanket rejection.", local, err)
	}
}
