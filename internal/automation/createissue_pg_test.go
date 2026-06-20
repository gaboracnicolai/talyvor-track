package automation

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// TestFire_CreateIssueAction_PersistsChildIssue_RealPG — end-to-end on real PG: the
// create_issue action persists a real, valid child issue linked to the parent and
// inheriting its creator. Before the fix this path posted a comment and created no
// issue at all.
func TestFire_CreateIssueAction_PersistsChildIssue_RealPG(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	parent := d.Issue(t, ws.ID, team.ID)
	issues := issue.NewStore(d.Pool)

	e := newEngine(d.Pool, issues, nil)
	withCachedRule(e, Rule{
		ID: "r-pg", WorkspaceID: ws.ID, TeamID: team.ID,
		Trigger:    TriggerStatusChanged,
		Actions:    []RuleAction{ActionCreateIssue},
		ActionData: map[string]string{"title": "Automated follow-up"},
	})

	if err := e.Fire(ctx, TriggerStatusChanged, ws.ID, *parent, nil); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM issues WHERE parent_id = $1`, parent.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("child issues with parent=%s: %d, want 1 (real issue persisted)", parent.ID, count)
	}

	var title, creator string
	var parentID *string
	if err := d.Pool.QueryRow(ctx,
		`SELECT title, creator_id, parent_id FROM issues WHERE parent_id = $1`, parent.ID).
		Scan(&title, &creator, &parentID); err != nil {
		t.Fatal(err)
	}
	if title != "Automated follow-up" {
		t.Errorf("child title = %q, want 'Automated follow-up'", title)
	}
	if creator != parent.CreatorID || parent.CreatorID == "" {
		t.Errorf("child creator = %q, want inherited %q", creator, parent.CreatorID)
	}
	if parentID == nil || *parentID != parent.ID {
		t.Errorf("child parent_id = %v, want %s", parentID, parent.ID)
	}
}
