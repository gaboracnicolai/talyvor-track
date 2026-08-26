package customfield_test

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

	"github.com/talyvor/track/internal/customfield"
	"github.com/talyvor/track/internal/testutil"
)

// TestCustomField_CrossWorkspaceTeamIsRefused_RealPG — a custom field may not be scoped to another
// workspace's team.
func TestCustomField_CrossWorkspaceTeamIsRefused_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	mine, theirs := d.Workspace(t), d.Workspace(t)
	localTeam, foreignTeam := d.Team(t, mine.ID), d.Team(t, theirs.ID)
	s := customfield.NewStore(d.Pool)

	if got, err := s.CreateField(ctx, customfield.CustomField{
		WorkspaceID: mine.ID, TeamID: &foreignTeam.ID, Name: "cross-tenant", Type: "text",
	}); err == nil {
		t.Errorf("CreateField ACCEPTED a team from another workspace (%s) — the ref guard is not "+
			"refusing. Row created as %v.", foreignTeam.ID, got.ID)
	}

	// MUST STAY GREEN.
	if _, err := s.CreateField(ctx, customfield.CustomField{
		WorkspaceID: mine.ID, TeamID: &localTeam.ID, Name: "same-workspace", Type: "text",
	}); err != nil {
		t.Fatalf("CreateField REFUSED the workspace's own team (%s): %v", localTeam.ID, err)
	}
}
