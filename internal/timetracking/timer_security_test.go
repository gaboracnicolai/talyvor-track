package timetracking_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/testutil"
	"github.com/talyvor/track/internal/timetracking"
)

// TestTimer_ObjectGraph_RejectsCrossWorkspaceIssue — a time entry must not reference
// an issue in a different workspace. StartTimer and LogTime both take a caller-
// supplied (issue_id, workspace_id); each must verify the issue belongs to that
// workspace. Real Postgres via the harness; members are seeded raw (FK-required).
func TestTimer_ObjectGraph_RejectsCrossWorkspaceIssue(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	tt := timetracking.NewStore(d.Pool)

	wsA := d.Workspace(t)
	issueA := d.Issue(t, wsA.ID, "")
	var memberA string
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO members (workspace_id, name, email) VALUES ($1, 'A', 'a@a.com') RETURNING id`,
		wsA.ID).Scan(&memberA); err != nil {
		t.Fatalf("seed member A: %v", err)
	}

	wsB := d.Workspace(t)
	var memberB string
	if err := d.Pool.QueryRow(ctx,
		`INSERT INTO members (workspace_id, name, email) VALUES ($1, 'B', 'b@b.com') RETURNING id`,
		wsB.ID).Scan(&memberB); err != nil {
		t.Fatalf("seed member B: %v", err)
	}

	// Positive control: a same-workspace timer works.
	if _, err := tt.StartTimer(ctx, issueA.ID, wsA.ID, memberA, "work"); err != nil {
		t.Fatalf("same-workspace StartTimer must succeed: %v", err)
	}

	// Cross-workspace: a WS-B time entry referencing a WS-A issue must be refused.
	if _, err := tt.StartTimer(ctx, issueA.ID, wsB.ID, memberB, "work"); err == nil {
		t.Error("LEAK: StartTimer created a WS-B time entry referencing a WS-A issue")
	}

	// LogTime (manual) is guarded the same way.
	now := time.Now()
	started := now.Add(-time.Hour)
	if _, err := tt.LogTime(ctx, timetracking.TimeEntry{
		IssueID: issueA.ID, WorkspaceID: wsB.ID, MemberID: memberB,
		StartedAt: started, StoppedAt: &now,
	}); err == nil {
		t.Error("LEAK: LogTime created a WS-B time entry referencing a WS-A issue")
	}
}
