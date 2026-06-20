package tenancy_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/featureboard"
	"github.com/talyvor/track/internal/milestone"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/scoring"
	"github.com/talyvor/track/internal/template"
	"github.com/talyvor/track/internal/testutil"
)

// TestCrossObjectTenancy_RepresentativeFamilies — one case per distinct cross-object
// reference family, all routed through tenancy.AssertRefInWorkspace: project_id
// (milestone), team_id (cycle), board_id (featureboard), templateID (template) and
// issue_id (scoring). Each seeds the reference in workspace A and the parent in
// workspace B; the cross-workspace link must be refused and the same-workspace one
// must work. Because every site funnels through the one primitive, neutering it
// turns ALL of these RED at once (see the package-level RED→GREEN proof).
func TestCrossObjectTenancy_RepresentativeFamilies(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	wsA := d.Workspace(t)
	wsB := d.Workspace(t)

	t.Run("project_id/milestone.Create", func(t *testing.T) {
		teamA := d.Team(t, wsA.ID)
		var projectA string
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO projects (workspace_id, team_id, name, identifier) VALUES ($1,$2,'PA','PA') RETURNING id`,
			wsA.ID, teamA.ID).Scan(&projectA); err != nil {
			t.Fatalf("seed project A: %v", err)
		}
		ms := milestone.NewStore(d.Pool)
		if _, err := ms.Create(ctx, milestone.Milestone{WorkspaceID: wsB.ID, ProjectID: projectA, Name: "v1"}); err == nil {
			t.Error("LEAK: milestone in WS-B accepted a project from WS-A")
		}
		teamB := d.Team(t, wsB.ID)
		var projectB string
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO projects (workspace_id, team_id, name, identifier) VALUES ($1,$2,'PB','PB') RETURNING id`,
			wsB.ID, teamB.ID).Scan(&projectB); err != nil {
			t.Fatalf("seed project B: %v", err)
		}
		if _, err := ms.Create(ctx, milestone.Milestone{WorkspaceID: wsB.ID, ProjectID: projectB, Name: "v1"}); err != nil {
			t.Errorf("same-workspace milestone must succeed: %v", err)
		}
	})

	t.Run("team_id/cycle.Create", func(t *testing.T) {
		teamA := d.Team(t, wsA.ID)
		cy := cycle.NewStore(d.Pool)
		now := time.Now()
		if _, err := cy.Create(ctx, model.Cycle{WorkspaceID: wsB.ID, TeamID: teamA.ID, Name: "S1", StartDate: now, EndDate: now.Add(48 * time.Hour)}); err == nil {
			t.Error("LEAK: cycle in WS-B accepted a team from WS-A")
		}
		teamB := d.Team(t, wsB.ID)
		if _, err := cy.Create(ctx, model.Cycle{WorkspaceID: wsB.ID, TeamID: teamB.ID, Name: "S2", StartDate: now, EndDate: now.Add(48 * time.Hour)}); err != nil {
			t.Errorf("same-workspace cycle must succeed: %v", err)
		}
	})

	t.Run("board_id/featureboard.CreatePost", func(t *testing.T) {
		fb := featureboard.NewStore(d.Pool)
		boardA, err := fb.CreateBoard(ctx, featureboard.Board{WorkspaceID: wsA.ID, Name: "BA", Slug: "ba", Public: true})
		if err != nil {
			t.Fatalf("seed board A: %v", err)
		}
		if _, err := fb.CreatePost(ctx, featureboard.FeaturePost{WorkspaceID: wsB.ID, BoardID: boardA.ID, Title: "x", AuthorEmail: "z@z.com"}); err == nil {
			t.Error("LEAK: post in WS-B accepted a board from WS-A")
		}
		boardB, err := fb.CreateBoard(ctx, featureboard.Board{WorkspaceID: wsB.ID, Name: "BB", Slug: "bb", Public: true})
		if err != nil {
			t.Fatalf("seed board B: %v", err)
		}
		if _, err := fb.CreatePost(ctx, featureboard.FeaturePost{WorkspaceID: wsB.ID, BoardID: boardB.ID, Title: "y", AuthorEmail: "z2@z.com"}); err != nil {
			t.Errorf("same-workspace post must succeed: %v", err)
		}
	})

	t.Run("templateID/template.ApplyTemplate", func(t *testing.T) {
		var tmplA string
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO issue_templates (workspace_id, name) VALUES ($1,'TA') RETURNING id`, wsA.ID).Scan(&tmplA); err != nil {
			t.Fatalf("seed template A: %v", err)
		}
		tm := template.NewStore(d.Pool)
		if err := tm.ApplyTemplate(ctx, tmplA, &model.Issue{WorkspaceID: wsB.ID}); err == nil {
			t.Error("LEAK: applied a WS-A template to a WS-B issue")
		}
		var tmplB string
		if err := d.Pool.QueryRow(ctx,
			`INSERT INTO issue_templates (workspace_id, name) VALUES ($1,'TB') RETURNING id`, wsB.ID).Scan(&tmplB); err != nil {
			t.Fatalf("seed template B: %v", err)
		}
		if err := tm.ApplyTemplate(ctx, tmplB, &model.Issue{WorkspaceID: wsB.ID}); err != nil {
			t.Errorf("same-workspace template apply must succeed: %v", err)
		}
	})

	t.Run("issue_id/scoring.SetScore", func(t *testing.T) {
		issueA := d.Issue(t, wsA.ID, "")
		sc := scoring.NewStore(d.Pool)
		rice := &scoring.RICEScore{Reach: 1, Impact: 1, Confidence: 50, Effort: 1}
		if _, err := sc.SetScore(ctx, issueA.ID, wsB.ID, "m", scoring.ScoringRICE, rice, nil, ""); err == nil {
			t.Error("LEAK: score in WS-B accepted an issue from WS-A")
		}
		issueB := d.Issue(t, wsB.ID, "")
		if _, err := sc.SetScore(ctx, issueB.ID, wsB.ID, "m", scoring.ScoringRICE, rice, nil, ""); err != nil {
			t.Errorf("same-workspace score must succeed: %v", err)
		}
	})
}
