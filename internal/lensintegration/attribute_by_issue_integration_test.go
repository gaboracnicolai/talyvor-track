package lensintegration_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// PER-ISSUE ATTRIBUTION — Track's half. Lens #401 is inert without this.
//
// Track credits an issue by matching a spend record against an issue IDENTIFIER. It matched on
// FEATURE, and the Code extension sends the feature as an IDE affordance ("code-chat"), so every
// request from the editor we ship credited nothing in the tracker we ship. Lens now also returns
// `issue_id` on /v1/api/spend/by-request (verified read-only: internal/api/server.go:732), captured
// from the X-Talyvor-Issue header the extension already sends.
//
// ⚠ ASSERTED ON THE LEDGER AND THE ROLLUP — one ai_spend_events row and issues.ai_cost_usd — never
// on a return value. resolved/landed are the function describing itself; the money is the two rows.

// seedIssue creates a workspace + one issue with the given identifier, and returns its id.
func seedIssue(t *testing.T, d *testutil.DB, wsID, identifier string) string {
	t.Helper()
	ctx := context.Background()
	_, _ = d.Pool.Exec(ctx, `INSERT INTO workspaces (id, name, slug) VALUES ($1,$1,$1)
		ON CONFLICT (id) DO NOTHING`, wsID)
	_, _ = d.Pool.Exec(ctx, `INSERT INTO teams (workspace_id, name, identifier) VALUES ($1,'T','T')
		ON CONFLICT DO NOTHING`, wsID)
	var teamID string
	if err := d.Pool.QueryRow(ctx, `SELECT id FROM teams WHERE workspace_id=$1 LIMIT 1`, wsID).Scan(&teamID); err != nil {
		t.Fatalf("team: %v", err)
	}
	st := issue.NewStore(d.Pool)
	out, err := st.Create(ctx, model.Issue{
		WorkspaceID: wsID, TeamID: teamID, Title: "t", CreatorID: "m-1", Identifier: identifier,
	})
	if err != nil {
		t.Fatalf("create issue %s: %v", identifier, err)
	}
	// Create derives its own identifier; force the one under test so the match is exact.
	if _, err := d.Pool.Exec(ctx, `UPDATE issues SET identifier=$1 WHERE id=$2`, identifier, out.ID); err != nil {
		t.Fatalf("set identifier: %v", err)
	}
	return out.ID
}

func ledger(t *testing.T, d *testutil.DB, issueID string) (rows int, cost float64) {
	t.Helper()
	ctx := context.Background()
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(SUM(cost_usd),0) FROM ai_spend_events WHERE issue_id=$1`,
		issueID).Scan(&rows, &cost); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return
}

func rollup(t *testing.T, d *testutil.DB, issueID string) float64 {
	t.Helper()
	var c float64
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT ai_cost_usd FROM issues WHERE id=$1`, issueID).Scan(&c); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	return c
}

// ⚠ THE CASE THAT WILL ACTUALLY HAPPEN: issue ENG-42 AND feature code-chat. Credits ENG-42.
// Before this change it credited nothing at all.
func TestAttribution_IssuePresent_CreditsTheIssue(t *testing.T) {
	d := testutil.New(t)
	const ws = "ws-attr-issue"
	id := seedIssue(t, d, ws, "ENG-42")
	st := issue.NewStore(d.Pool)

	resolved, landed, err := st.RecordRequestSpendAttributed(context.Background(),
		"req-1", "code-chat", "ENG-42", 0.25, 100, ws)
	if err != nil {
		t.Fatalf("RecordRequestSpendAttributed: %v", err)
	}
	if !resolved || !landed {
		t.Fatalf("resolved=%v landed=%v — expected the issue to resolve and the cost to land", resolved, landed)
	}
	if n, c := ledger(t, d, id); n != 1 || c != 0.25 {
		t.Errorf("ai_spend_events for ENG-42: rows=%d cost=%v, want 1 row of 0.25 — the editor's "+
			"spend must reach the issue the user was working on", n, c)
	}
	if got := rollup(t, d, id); got != 0.25 {
		t.Errorf("issues.ai_cost_usd = %v, want 0.25", got)
	}
}

// ⚠ THE FALLBACK, TESTED ON ITS OWN. Manual taggers set X-Talyvor-Feature: ENG-42 and send no issue
// header. They work today and must keep working; a single test covering both paths proves neither.
func TestAttribution_IssueEmpty_FallsBackToFeature(t *testing.T) {
	d := testutil.New(t)
	const ws = "ws-attr-feature"
	id := seedIssue(t, d, ws, "ENG-7")
	st := issue.NewStore(d.Pool)

	resolved, landed, err := st.RecordRequestSpendAttributed(context.Background(),
		"req-2", "ENG-7", "", 0.5, 50, ws)
	if err != nil {
		t.Fatalf("RecordRequestSpendAttributed: %v", err)
	}
	if !resolved || !landed {
		t.Fatalf("resolved=%v landed=%v — the manual-tagging path must still credit", resolved, landed)
	}
	if n, c := ledger(t, d, id); n != 1 || c != 0.5 {
		t.Errorf("fallback path: rows=%d cost=%v, want 1 row of 0.5 — manual taggers regressed", n, c)
	}
	if got := rollup(t, d, id); got != 0.5 {
		t.Errorf("issues.ai_cost_usd = %v, want 0.5", got)
	}
}

// ⚠ THE ISSUE WINS WHEN BOTH RESOLVE. Otherwise a feature that happens to name a different issue
// would silently outrank the issue the user was actually on.
func TestAttribution_IssueBeatsFeatureWhenBothMatch(t *testing.T) {
	d := testutil.New(t)
	const ws = "ws-attr-both"
	want := seedIssue(t, d, ws, "ENG-100")
	other := seedIssue(t, d, ws, "ENG-200")
	st := issue.NewStore(d.Pool)

	if _, _, err := st.RecordRequestSpendAttributed(context.Background(),
		"req-3", "ENG-200", "ENG-100", 0.75, 10, ws); err != nil {
		t.Fatalf("record: %v", err)
	}
	if n, _ := ledger(t, d, want); n != 1 {
		t.Errorf("ENG-100 (the issue header) got %d rows, want 1", n)
	}
	if n, _ := ledger(t, d, other); n != 0 {
		t.Errorf("ENG-200 (the feature) got %d rows, want 0 — the feature outranked the issue", n)
	}
}

// ⚠ EXACTLY-ONCE. The syncer re-reads the same 24h window ~96x/day; a re-pull must credit nothing.
func TestAttribution_RePullCreditsNothing(t *testing.T) {
	d := testutil.New(t)
	const ws = "ws-attr-once"
	id := seedIssue(t, d, ws, "ENG-9")
	st := issue.NewStore(d.Pool)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if _, _, err := st.RecordRequestSpendAttributed(ctx, "req-same", "code-chat", "ENG-9", 0.3, 5, ws); err != nil {
			t.Fatalf("pull %d: %v", i, err)
		}
	}
	if n, c := ledger(t, d, id); n != 1 || c != 0.3 {
		t.Errorf("after 4 pulls of the same request: rows=%d cost=%v, want 1 row of 0.3 — "+
			"the window is re-read ~96x/day and every re-read would double the bill", n, c)
	}
	if got := rollup(t, d, id); got != 0.3 {
		t.Errorf("issues.ai_cost_usd = %v after 4 pulls, want 0.3", got)
	}
}

// ⚠ #66's RULE, ON THE BRANCH THIS CHANGE TOUCHES: no match by EITHER field ⇒ the money is still
// RECORDED, with a NULL issue_id. Never dropped, never guessed onto a wrong issue.
func TestAttribution_NeitherMatches_RecordedAsUnattributed(t *testing.T) {
	d := testutil.New(t)
	const ws = "ws-attr-none"
	present := seedIssue(t, d, ws, "ENG-1")
	st := issue.NewStore(d.Pool)

	if _, _, err := st.RecordRequestSpendAttributed(context.Background(),
		"req-4", "code-chat", "NOPE-999", 0.9, 7, ws); err != nil {
		t.Fatalf("record: %v", err)
	}
	var n int
	var cost float64
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(SUM(cost_usd),0) FROM ai_spend_events
		 WHERE workspace_id=$1 AND issue_id IS NULL`, ws).Scan(&n, &cost); err != nil {
		t.Fatalf("unattributed: %v", err)
	}
	if n != 1 || cost != 0.9 {
		t.Errorf("unattributed rows=%d cost=%v, want 1 row of 0.9 — spend that matches nothing must "+
			"still be recorded, or it silently disappears from the ledger", n, cost)
	}
	if m, _ := ledger(t, d, present); m != 0 {
		t.Errorf("an unmatched request was credited to ENG-1 (%d rows) — money guessed onto a wrong issue", m)
	}
}
