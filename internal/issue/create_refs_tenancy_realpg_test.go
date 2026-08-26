package issue_test

// create_refs_tenancy_realpg_test.go — issue.Store.Create GUARDS FIVE CROSS-OBJECT REFERENCES AND
// FOUR OF THE FIVE GUARDS WERE HELD BY NOTHING.
//
// Create refuses a project / cycle / assignee / parent / milestone that belongs to another
// workspace, by looping a literal field list and calling assertRefInWorkspace on each. MEASURED at
// ae57b43, one field dropped from THAT list at a time, whole repository + semgrep:
//
//	project_id    semgrep NOT CAUGHT · go test NOT CAUGHT
//	cycle_id      semgrep NOT CAUGHT · go test NOT CAUGHT
//	assignee_id   semgrep NOT CAUGHT · go test NOT CAUGHT
//	parent_id     semgrep NOT CAUGHT · go test NOT CAUGHT
//	milestone_id  semgrep NOT CAUGHT · go test CAUGHT (milestone_attach_realpg_test.go)
//
// The one that IS covered is what proves the instrument can see a catch at all, so this census
// carries its own positive control by construction rather than by a separate case.
//
// ⚠ WHY SEMGREP IS BLIND TO ALL FIVE. .semgrep/cross-object-tenancy.yml exempts the enclosing
// function when `assertRefInWorkspace(` APPEARS IN IT. That asserts the call is PRESENT and
// nothing about WHICH references it covers — so one guarded field buys the exemption for every
// field beside it. This is the same shape as #178's finding one level up: a rule keyed on a
// construct being present cannot tell you which instance it is exempting.
//
// ⚠ AND refs_security_test.go's HEADER MADE A COVERAGE CLAIM THAT MEASUREMENT REFUTES:
// "project_id / cycle_id ride the identical validation path". They do on UPDATE —
// validateRefWorkspaces iterates the SHARED issueRefQueries map — but CREATE has its own LITERAL
// field list, a second copy. A field present in one and absent from the other is exactly what
// nothing was checking, which is why that file's parent_id/assignee_id cases (both Update-only)
// left Create's list unheld. The claim is corrected in that file rather than deleted.
//
// EVERY CASE HAS A MUST-STAY-GREEN COMPANION using the workspace's OWN object. Without it a
// refusal assertion also passes on a Create that rejects the field unconditionally — "you may
// never set a project" would satisfy the tenancy assertion while being a different bug.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

func seedProject(t *testing.T, d *testutil.DB, wsID, teamID, ident string) *model.Project {
	t.Helper()
	p, err := project.NewStore(d.Pool).Create(context.Background(), model.Project{
		WorkspaceID: wsID, TeamID: teamID, Name: "P " + ident, Identifier: ident,
	})
	if err != nil {
		t.Fatalf("seed project %s: %v", ident, err)
	}
	return p
}

func seedCycle(t *testing.T, d *testutil.DB, wsID, teamID, name string) *model.Cycle {
	t.Helper()
	start := time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)
	c, err := cycle.NewStore(d.Pool).Create(context.Background(), model.Cycle{
		WorkspaceID: wsID, TeamID: teamID, Name: name,
		StartDate: start, EndDate: start.AddDate(0, 0, 14),
	})
	if err != nil {
		t.Fatalf("seed cycle %s: %v", name, err)
	}
	return c
}

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

// TestCreate_CrossWorkspaceRefsAreRefused_RealPG drives Create once per reference with a foreign
// object and once with a local one. The refusal must NAME the field: "Create returned an error" is
// satisfied by any failure, including a fixture that was broken all along.
func TestCreate_CrossWorkspaceRefsAreRefused_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	issues := issue.NewStore(d.Pool)

	mine, theirs := d.Workspace(t), d.Workspace(t)
	myTeam, theirTeam := d.Team(t, mine.ID), d.Team(t, theirs.ID)

	localProject, foreignProject := seedProject(t, d, mine.ID, myTeam.ID, "MINE"), seedProject(t, d, theirs.ID, theirTeam.ID, "THRS")
	localCycle, foreignCycle := seedCycle(t, d, mine.ID, myTeam.ID, "mine"), seedCycle(t, d, theirs.ID, theirTeam.ID, "theirs")
	localMember, foreignMember := seedMember(t, d, mine.ID, "mine@x.com"), seedMember(t, d, theirs.ID, "theirs@x.com")
	localParent, foreignParent := d.Issue(t, mine.ID, myTeam.ID), d.Issue(t, theirs.ID, theirTeam.ID)

	cases := []struct {
		field          string
		local, foreign string
		set            func(*model.Issue, string)
	}{
		{"project_id", localProject.ID, foreignProject.ID, func(i *model.Issue, v string) { i.ProjectID = &v }},
		{"cycle_id", localCycle.ID, foreignCycle.ID, func(i *model.Issue, v string) { i.CycleID = &v }},
		{"assignee_id", localMember, foreignMember, func(i *model.Issue, v string) { i.AssigneeID = &v }},
		{"parent_id", localParent.ID, foreignParent.ID, func(i *model.Issue, v string) { i.ParentID = &v }},
	}

	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			// ── THE REFUSAL. A reference owned by `theirs`, on an issue created in `mine`.
			bad := model.Issue{WorkspaceID: mine.ID, TeamID: myTeam.ID,
				Title: "cross-tenant " + c.field, CreatorID: "human"}
			c.set(&bad, c.foreign)
			got, err := issues.Create(ctx, bad)
			if err == nil {
				t.Errorf("Create ACCEPTED a %s from another workspace (%s) — the object-graph guard "+
					"for this field is not running. The issue was created as %v.", c.field, c.foreign, got.ID)
			} else if !strings.Contains(err.Error(), c.field) {
				t.Errorf("Create refused, but not by naming %s: %v. A refusal that does not name the "+
					"field cannot be told apart from an unrelated failure — including a broken fixture.",
					c.field, err)
			}

			// ── THE COMPANION, MUST STAY GREEN. The same call with the workspace's OWN object must
			// SUCCEED. Without it the assertion above also passes on a Create that refuses this
			// field for everyone, which is a different bug wearing the same red.
			good := model.Issue{WorkspaceID: mine.ID, TeamID: myTeam.ID,
				Title: "same-workspace " + c.field, CreatorID: "human"}
			c.set(&good, c.local)
			if _, err := issues.Create(ctx, good); err != nil {
				t.Fatalf("Create REFUSED the workspace's own %s (%s): %v — the refusal above is not "+
					"a tenancy check, it is a blanket rejection of this field.", c.field, c.local, err)
			}
		})
	}
}
