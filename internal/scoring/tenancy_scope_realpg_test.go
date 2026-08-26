package scoring_test

// THE SCORING STORE'S WORKSPACE PREDICATES CAN BE MADE INERT WITHOUT ARITY CHANGING, AND BEFORE
// THIS FILE THE ENTIRE REPOSITORY SUITE STAYED GREEN WHEN THEY WERE.
//
// ⚠ READ THIS FIRST, BECAUSE THIS FILE PASSES ON MAIN AND A GUARD THAT PASSES ON THE FIRST RUN IS
// THE THING THIS QUEUE DISTRUSTS MOST. The predicates are CORRECT today. Nothing here is a live
// cross-tenant read. What was missing is not the scope — it is any assertion that the scope still
// does something, and a scope nobody has watched refuse is a scope nobody knows still refuses.
// This is therefore a CHARACTERISATION guard whose entire worth is its positive controls:
// scripts/w310-scoring-tenancy-controls-m5x8.py. If those arms ever stop going red, delete this
// file — it will have become decoration.
//
// ⚠ HOW IT WAS FOUND, because the route matters more than the finding. It came out of W3.9's C5
// arm — the SPECIFICITY arm, the one designed to be caught by nobody's new test and by the
// pre-existing suite. It scored "AS PREDICTED", because the prediction was only ever about the new
// file, and the harness printed `pre-existing caught: (none)` underneath. "As predicted" is a
// verdict on the prediction, not on the codebase.
//
// ⚠ AND CHASING IT TOOK THREE MUTATIONS, NOT ONE, BECAUSE THE FIRST TWO WOULD HAVE OVERSTATED IT:
//
//	  1. SQL-only (`AND workspace_id = $2` deleted, argument left)   NOT a silent defect. Postgres
//	     refuses the surplus bind parameter outright — measured, not assumed:
//	     "wrong number of parameters for prepared statement / Expected 1 parameters but got 2".
//	     It could never reach production.
//	  2. Realistic (predicate AND argument both deleted)             CAUGHT — but only by pgxmock's
//	     "expected 2, but got 1 arguments". That is an ARITY assertion wearing a tenancy
//	     assertion's clothes: it fires on the shape of the call, never on who is refused.
//	  3. ARITY-PRESERVING (`workspace_id = $2` -> `($2 = $2)`)       THE FINDING. Applied to
//	     GetScore AND DeleteScore at once, `go test -count=1 ./...` stayed green across every
//	     package in the repository. It runs fine on real Postgres, and it removes both scopes.
//
// ⚠ AND THE LOCK THAT LOOKS LIKE IT COVERS THIS DOES NOT. CI's tenancy-lock runs .semgrep/ over
// internal/ + cmd/, and its two rules are (a) a store INSERT writing a cross-object *_id without
// tenancy.AssertRefInWorkspace and (b) a /v1 handler taking its workspace from chi.URLParam("wsID"),
// the spoofable X-Member-Id, or a flat ?workspace_id=. Neither is an inert predicate inside a store
// READ. The handlers here are correct — they take the workspace from authz.WorkspaceID(r.Context()).
// It is the predicate they hand down that had no guard. SetScore is genuinely covered, by
// tenancy.AssertRefInWorkspace and therefore by rule (a); the read and delete paths were not.
//
// ⚠ WHAT THIS FILE DELIBERATELY PINS RATHER THAN FIXES — two measured facts, neither a defect:
//
//   - IssueScores(ctx, issueID) HAS NO WORKSPACE PREDICATE AND TAKES NO WORKSPACE ARGUMENT. It sits
//     between GetScore and DeleteScore, both of which carry an explicit "SEC-5: scoped to the
//     caller's workspace" comment, and it has neither the scope nor a comment saying why. Censused:
//     its ONE production call site is issue.Store.attachScores, reached from GetByID and
//     GetByIdentifier, and the handlers that call those re-check the workspace themselves after the
//     fetch (guest/handler.go and lensintegration/handler.go both do, measured). So the scoping is
//     INHERITED, not absent — but it is inherited from callers this package cannot see, and nothing
//     says so. Pinned below so that stops being true silently.
//
//   - DELETING ANOTHER WORKSPACE'S SCORE ANSWERS HTTP 200 {"ok":true}. DeleteScore discards the
//     command tag, so "removed one row" and "matched nothing" are byte-identical to the handler.
//     For a FOREIGN id that is defensible — a 404 would be an existence oracle — and it is not
//     being changed here. But it means the refusal is invisible from the outside, which is exactly
//     why the assertion below counts the surviving ROW instead of trusting the error.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/talyvor/track/internal/scoring"
	"github.com/talyvor/track/internal/testutil"
)

// scoreRowCount reads the table directly. DeleteScore returns only an error, so the ONLY way to
// tell a refused delete from a successful one is to look at what is still there.
func scoreRowCount(t *testing.T, d *testutil.DB, issueID string) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM issue_scores WHERE issue_id = $1`, issueID).Scan(&n); err != nil {
		t.Fatalf("count issue_scores for %s: %v", issueID, err)
	}
	return n
}

// twoWorkspaces seeds workspace A with one scored issue and workspace B with one scored issue, and
// returns (wsA, issueA, wsB, issueB). B's score is HIGHER so that a lost scope shows up as the
// wrong VALUE and not merely as an extra row.
func twoWorkspaces(t *testing.T, d *testutil.DB) (string, string, string, string) {
	t.Helper()
	wsA := d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	issueA := d.Issue(t, wsA.ID, teamA.ID)
	seedScore(t, d, issueA.ID, wsA.ID, "rice", f(11), nil)

	wsB := d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	issueB := d.Issue(t, wsB.ID, teamB.ID)
	seedScore(t, d, issueB.ID, wsB.ID, "rice", f(97), nil)

	return wsA.ID, issueA.ID, wsB.ID, issueB.ID
}

// TestMeasured_ScoringStore_RefusesAnotherWorkspacesIssue is the regression guard. Each arm names a
// DIFFERENT statement, so no one arm can stand in for another.
func TestMeasured_ScoringStore_RefusesAnotherWorkspacesIssue(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := scoring.NewStore(d.Pool)

	wsA, issueA, wsB, _ := twoWorkspaces(t, d)

	t.Run("GetScore does not disclose it", func(t *testing.T) {
		got, err := store.GetScore(ctx, issueA, wsB)
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetScore(issue of A, as B) = (%+v, %v), want pgx.ErrNoRows.\n"+
				"A red here means the workspace predicate on GetScore stopped refusing.", got, err)
		}
	})

	t.Run("DeleteScore leaves the row where it is", func(t *testing.T) {
		if n := scoreRowCount(t, d, issueA); n != 1 {
			t.Fatalf("precondition: A's score row count = %d, want 1", n)
		}
		if err := store.DeleteScore(ctx, issueA, wsB); err != nil {
			t.Fatalf("DeleteScore(issue of A, as B) errored: %v", err)
		}
		// ⚠ THE ERROR BEING nil IS NOT THE ASSERTION. DeleteScore discards the command tag, so a
		// refused delete and a successful one are indistinguishable from its return value. The row
		// is the only witness.
		if n := scoreRowCount(t, d, issueA); n != 1 {
			t.Fatalf("A's score row count after B's delete = %d, want 1 — B DELETED ANOTHER "+
				"WORKSPACE'S SCORE and the call returned a nil error while doing it", n)
		}
	})

	t.Run("GetPrioritizedBacklog never lists it", func(t *testing.T) {
		out, err := store.GetPrioritizedBacklog(ctx, wsB, nil, "rice", 50)
		if err != nil {
			t.Fatalf("GetPrioritizedBacklog: %v", err)
		}
		for _, si := range out {
			if si.ID == issueA {
				t.Fatalf("B's backlog contains A's issue %s", issueA)
			}
			if si.WorkspaceID != wsB {
				t.Fatalf("B's backlog contains an issue from workspace %s", si.WorkspaceID)
			}
		}
	})

	t.Run("GetScoreSummary counts only its own", func(t *testing.T) {
		sum, err := store.GetScoreSummary(ctx, wsB)
		if err != nil {
			t.Fatalf("GetScoreSummary: %v", err)
		}
		if sum.TotalScored != 1 {
			t.Errorf("B total_scored = %d, want 1 (its own, not A's too)", sum.TotalScored)
		}
		if sum.TopIssueID == issueA {
			t.Errorf("B's top_issue_id is A's issue %s", issueA)
		}
		if sum.AvgRICE < 96.9 || sum.AvgRICE > 97.1 {
			t.Errorf("B avg_rice = %v, want 97 — 54 would mean it averaged A's 11 in too", sum.AvgRICE)
		}
	})

	// Nothing above may have disturbed A.
	if n := scoreRowCount(t, d, issueA); n != 1 {
		t.Fatalf("A's score did not survive the whole test: count = %d", n)
	}
	if _, err := store.GetScore(ctx, issueA, wsA); err != nil {
		t.Fatalf("A can no longer read its own score: %v", err)
	}
}

// TestMeasured_ScoringStore_AllowsItsOwnWorkspace is the must-stay-green control. It is what stops
// a "fix" that simply refuses everything from passing the file above.
func TestMeasured_ScoringStore_AllowsItsOwnWorkspace(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := scoring.NewStore(d.Pool)

	wsA, issueA, _, _ := twoWorkspaces(t, d)

	got, err := store.GetScore(ctx, issueA, wsA)
	if err != nil {
		t.Fatalf("GetScore(own issue, own workspace): %v", err)
	}
	if got.IssueID != issueA {
		t.Errorf("issue_id = %q, want %q", got.IssueID, issueA)
	}
	if got.RICE == nil || got.RICE.Score < 10.9 || got.RICE.Score > 11.1 {
		t.Errorf("rice score = %+v, want 11", got.RICE)
	}

	backlog, err := store.GetPrioritizedBacklog(ctx, wsA, nil, "rice", 50)
	if err != nil {
		t.Fatalf("GetPrioritizedBacklog: %v", err)
	}
	if len(backlog) == 0 {
		t.Fatalf("A's own backlog is empty — a refusal that refuses its owner is not a scope")
	}

	if err := store.DeleteScore(ctx, issueA, wsA); err != nil {
		t.Fatalf("DeleteScore(own): %v", err)
	}
	if n := scoreRowCount(t, d, issueA); n != 0 {
		t.Fatalf("A deleting its OWN score left %d rows — the scope now refuses everybody", n)
	}
}

// TestCharacterised_IssueScores_ReadsByBareIDWithNoWorkspaceScope pins today's behaviour on
// purpose, so it PASSES on main. It is not an endorsement: it records that this one reader is
// scoped only by its callers, so that fact cannot change — in either direction — in silence.
//
// ⚠ IF THIS GOES RED, A WORKSPACE SCOPE WAS ADDED TO IssueScores. That is very likely an
// improvement. Rewrite this test to pin the NEW rule; do not delete it to make the change green.
func TestCharacterised_IssueScores_ReadsByBareIDWithNoWorkspaceScope(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	store := scoring.NewStore(d.Pool)

	_, issueA, _, _ := twoWorkspaces(t, d)

	// There is no workspace argument to get wrong — that is the whole point.
	rice, ice, err := store.IssueScores(ctx, issueA)
	if err != nil {
		t.Fatalf("IssueScores: %v", err)
	}
	if rice == nil || *rice < 10.9 || *rice > 11.1 {
		t.Fatalf("IssueScores by bare id returned rice=%v (want 11) — if this is now nil because a "+
			"workspace scope was added, that is the improvement landing: re-pin, do not delete", rice)
	}
	if ice != nil {
		t.Errorf("ice = %v, want nil for a RICE-only score", *ice)
	}
}
