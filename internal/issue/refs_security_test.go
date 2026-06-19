package issue_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// TestIssueRefs_ObjectGraph_RejectCrossWorkspace — settable cross-object references
// (parent_id / assignee_id on Update, team_id on Create) must point at an object in
// the issue's OWN workspace; a reference from another workspace is refused, while a
// same-workspace reference still works. project_id / cycle_id ride the identical
// validation path. Real Postgres via the harness.
func TestIssueRefs_ObjectGraph_RejectCrossWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	iss := issue.NewStore(d.Pool)

	wsA := d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	parentA := d.Issue(t, wsA.ID, teamA.ID) // potential cross-workspace parent
	var memberA string
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO members (workspace_id, name, email) VALUES ($1, 'A', 'a@a.com') RETURNING id`,
		wsA.ID).Scan(&memberA); err != nil {
		t.Fatalf("seed member A: %v", err)
	}

	wsB := d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	issueB := d.Issue(t, wsB.ID, teamB.ID)  // the victim issue, in WS-B
	parentB := d.Issue(t, wsB.ID, teamB.ID) // same-workspace parent (control)
	var memberB string
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO members (workspace_id, name, email) VALUES ($1, 'B', 'b@b.com') RETURNING id`,
		wsB.ID).Scan(&memberB); err != nil {
		t.Fatalf("seed member B: %v", err)
	}

	// (Update) cross-workspace parent_id must be refused.
	if _, err := iss.Update(ctx, issueB.ID, map[string]any{"parent_id": parentA.ID}); err == nil {
		t.Error("LEAK: issueB (WS-B) accepted a parent_id from WS-A")
	}
	// (Update) cross-workspace assignee_id must be refused.
	if _, err := iss.Update(ctx, issueB.ID, map[string]any{"assignee_id": memberA}); err == nil {
		t.Error("LEAK: issueB (WS-B) accepted an assignee_id from WS-A")
	}
	// (Update) positive controls: same-workspace references work.
	if _, err := iss.Update(ctx, issueB.ID, map[string]any{"parent_id": parentB.ID}); err != nil {
		t.Errorf("same-workspace parent_id must be accepted: %v", err)
	}
	if _, err := iss.Update(ctx, issueB.ID, map[string]any{"assignee_id": memberB}); err != nil {
		t.Errorf("same-workspace assignee_id must be accepted: %v", err)
	}

	// (Create) cross-workspace team_id must be refused.
	if _, err := iss.Create(ctx, model.Issue{
		WorkspaceID: wsB.ID, TeamID: teamA.ID, Title: "x", CreatorID: "c",
	}); err == nil {
		t.Error("LEAK: created an issue in WS-B with a team from WS-A")
	}
	// (Create) positive control: same-workspace team works.
	if _, err := iss.Create(ctx, model.Issue{
		WorkspaceID: wsB.ID, TeamID: teamB.ID, Title: "y", CreatorID: "c",
	}); err != nil {
		t.Errorf("same-workspace create must succeed: %v", err)
	}
}
