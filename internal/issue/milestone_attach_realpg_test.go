package issue_test

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

// milestone_attach_realpg_test.go — the THREE doors onto issues.milestone_id, and the one that has
// to stay shut.
//
// The roadmap's structurally-zero milestone counters (internal/project/roadmap_milestone_realpg_test.go)
// were caused by a column no write path named. Fixing only the PATCH allowlist would have left
// `POST /v1/issues` — which decodes the whole model.Issue off the body — accepting a milestone_id
// into an INSERT that never wrote it: the same 200-with-nothing-stored, one route over. That is the
// shape #93 found in talyvor-docs ("I shut one door onto a money column and read the column as
// closed; there were two"), and it is why the Create case below exists as its own assertion rather
// than as a variation of the Update one.
//
// The third door is the RE-IMPORT, and it must stay shut: no transport maps a milestone, so
// EXCLUDED.milestone_id is always NULL and clobbering it would erase a human's attachment on every
// re-import while importing nothing.

func seedMilestone(t *testing.T, db *testutil.DB, wsID, teamID, name string) *milestone.Milestone {
	t.Helper()
	ctx := context.Background()
	proj, err := project.NewStore(db.Pool).Create(ctx, model.Project{
		WorkspaceID: wsID, TeamID: teamID, Name: "P " + name, Identifier: "P" + name,
	})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	m, err := milestone.NewStore(db.Pool).Create(ctx, milestone.Milestone{
		WorkspaceID: wsID, ProjectID: proj.ID, Name: name,
	})
	if err != nil {
		t.Fatalf("seed milestone: %v", err)
	}
	return m
}

// THE CREATE DOOR. handler.Create decodes model.Issue from the request body, so a milestone_id that
// the INSERT does not name is accepted and dropped in silence.
func TestCreate_StoresTheMilestoneItWasGiven_RealPG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.New(t)
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	ms := seedMilestone(t, db, ws.ID, tm.ID, "M1")

	created, err := issue.NewStore(db.Pool).Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: tm.ID, Title: "filed against a milestone",
		CreatorID: "human", MilestoneID: &ms.ID,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The echo: a caller that cannot read the field back cannot tell a stored value from a dropped one.
	if created.MilestoneID == nil || *created.MilestoneID != ms.ID {
		t.Errorf("Create returned milestone_id %v, want %q", created.MilestoneID, ms.ID)
	}
	// The column, read straight from the table.
	var stored *string
	if err := db.Pool.QueryRow(ctx, `SELECT milestone_id FROM issues WHERE id = $1`, created.ID).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored == nil || *stored != ms.ID {
		t.Errorf("issues.milestone_id = %v, want %q — Create accepted it and did not write it", stored, ms.ID)
	}
	// And the scoped READ carries it too, so GET /v1/issues/{id} can show what was attached.
	got, err := issue.NewStore(db.Pool).GetInWorkspace(ctx, created.ID, ws.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.MilestoneID == nil || *got.MilestoneID != ms.ID {
		t.Errorf("GetInWorkspace returned milestone_id %v, want %q", got.MilestoneID, ms.ID)
	}
}

// THE TENANCY GATE, on both doors. A milestone from another workspace must be refused, exactly as
// the four sibling references are — object-graph integrity is the whole reason issueRefQueries
// exists, and a fifth entry that is in updatableFields but not in that map would be a hole rather
// than a feature.
func TestMilestoneFromAnotherWorkspaceIsRefused_RealPG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.New(t)
	mine, theirs := db.Workspace(t), db.Workspace(t)
	myTeam, theirTeam := db.Team(t, mine.ID), db.Team(t, theirs.ID)
	foreign := seedMilestone(t, db, theirs.ID, theirTeam.ID, "Theirs")
	local := seedMilestone(t, db, mine.ID, myTeam.ID, "Mine")
	issues := issue.NewStore(db.Pool)

	// Create must refuse it...
	if _, err := issues.Create(ctx, model.Issue{
		WorkspaceID: mine.ID, TeamID: myTeam.ID, Title: "cross-tenant attach",
		CreatorID: "human", MilestoneID: &foreign.ID,
	}); err == nil {
		t.Error("Create accepted a milestone from another workspace")
	} else if !strings.Contains(err.Error(), "milestone_id") {
		t.Errorf("Create refused, but not by naming the field: %v", err)
	}

	// ...and so must Update.
	iss := db.Issue(t, mine.ID, myTeam.ID)
	if _, err := issues.Update(ctx, iss.ID, mine.ID, map[string]any{"milestone_id": foreign.ID}); err == nil {
		t.Error("Update accepted a milestone from another workspace")
	}
	var stored *string
	if err := db.Pool.QueryRow(ctx, `SELECT milestone_id FROM issues WHERE id = $1`, iss.ID).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != nil {
		t.Errorf("the refused Update still wrote milestone_id = %q", *stored)
	}

	// THE COMPANION POSITIVE: the same call with the workspace's OWN milestone must SUCCEED, so the
	// refusal above cannot be a blanket "milestone_id is never writable" passing itself off as a
	// tenancy check.
	if _, err := issues.Update(ctx, iss.ID, mine.ID, map[string]any{"milestone_id": local.ID}); err != nil {
		t.Fatalf("Update refused the workspace's OWN milestone: %v", err)
	}
}

// THE RE-IMPORT DOOR, WHICH MUST STAY SHUT. UpsertByIdentifier's conflict arm omits milestone_id on
// purpose: no transport maps one, so EXCLUDED.milestone_id is always NULL and clobbering would erase
// a human's attachment on every re-import while importing nothing. The three columns the arm DOES
// clobber are asserted alongside it, so this test fails if the omission ever becomes an accident of
// the whole SET list going missing.
func TestReimport_DoesNotEraseAMilestoneAHumanAttached_RealPG(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.New(t)
	ws := db.Workspace(t)
	tm := db.Team(t, ws.ID)
	ms := seedMilestone(t, db, ws.ID, tm.ID, "M2")
	issues := issue.NewStore(db.Pool)

	imported, _, err := issues.UpsertByIdentifier(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: tm.ID, Identifier: "PROJ-1", Title: "first import",
		Description: "v1", CreatorID: model.ImporterCreatorID, Labels: []string{"a"},
	})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if _, err := issues.Update(ctx, imported.ID, ws.ID, map[string]any{"milestone_id": ms.ID}); err != nil {
		t.Fatalf("human attaches the imported issue to a milestone: %v", err)
	}

	// The SAME provider row again, carrying no milestone (no transport maps one) and new text.
	again, inserted, err := issues.UpsertByIdentifier(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: tm.ID, Identifier: "PROJ-1", Title: "second import",
		Description: "v2", CreatorID: model.ImporterCreatorID, Labels: []string{"b"},
	})
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if inserted {
		t.Fatal("control failed: the re-import INSERTed a second row, so nothing was preserved or clobbered")
	}
	if again.MilestoneID == nil || *again.MilestoneID != ms.ID {
		t.Errorf("re-import left milestone_id = %v, want %q — a provider that maps no milestone "+
			"erased the one a human attached", again.MilestoneID, ms.ID)
	}
	// The control on the other side: the columns the arm is supposed to clobber DID move, so the
	// preservation above is a decision this statement makes rather than an UPDATE that did nothing.
	if again.Title != "second import" || again.Description != "v2" {
		t.Errorf("control failed: the re-import clobbered neither title nor description (%q/%q) — "+
			"this test would pass on a statement that updates nothing at all", again.Title, again.Description)
	}
}
