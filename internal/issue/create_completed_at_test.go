package issue_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// create_completed_at_test.go — Create's INSERT does not name `completed_at`.
//
// FOUND FROM THE IMPORT SIDE (W3.4), but it is a store defect and it is pinned here, next to the
// statement, rather than only in the importer package. #74 measured the importer's UPSERT naming
// `due_date` and NOT naming `completed_at`, and fixed that one statement. Create is the SECOND COPY
// OF THAT SEAM — it is the write path for every CSV import row (a CSV row carries no provider
// identifier, so run() never reaches the upsert) and for every natively created issue. Measured on
// ba5d90a: the INSERT names due_date and omits completed_at entirely, so a caller-supplied
// CompletedAt is discarded in silence and the returned issue reports it as nil.
//
// ⚠ THE COLUMN IS NOT SIMPLY ADDED, BECAUSE Create's BODY IS PUBLIC API AND MODEL-SHAPED.
// handler.Create decodes the whole model.Issue off the request body, so naming completed_at in the
// INSERT with no rule would newly let any API client file a BACKLOG issue carrying a completion
// time. That state is one NO Track path can produce — Update stamps completed_at only on a
// transition ONTO done and CLEARS it on any transition away — and analytics' resolution-stats query
// selects on `completed_at IS NOT NULL` with NO status predicate, so such a row is counted as
// delivered work in cycle time and throughput. Create therefore records a completion time only on a
// row that is `done`, which is Update's invariant stated once more at the other write path.

func createIssue(t *testing.T, s *issue.Store, wsID, teamID string, i model.Issue) *model.Issue {
	t.Helper()
	i.WorkspaceID, i.TeamID, i.CreatorID = wsID, teamID, model.ImporterCreatorID
	got, err := s.Create(context.Background(), i)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return got
}

func readCompletedAt(t *testing.T, d *testutil.DB, issueID string) *time.Time {
	t.Helper()
	var out *time.Time
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT completed_at FROM issues WHERE id = $1`, issueID).Scan(&out); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	return out
}

// A done issue created with a completion time keeps it — in the COLUMN, not just in the struct the
// caller handed in.
func TestCreate_PersistsCompletedAtOnADoneIssue(t *testing.T) {
	d := testutil.New(t)
	s := issue.NewStore(d.Pool)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	when := time.Date(2025, 3, 25, 10, 3, 0, 0, time.UTC)
	got := createIssue(t, s, ws.ID, team.ID, model.Issue{
		Title: "Shipped", Status: model.StatusDone, CompletedAt: &when,
	})

	stored := readCompletedAt(t, d, got.ID)
	if stored == nil {
		t.Fatalf("completed_at IS NULL in Postgres; Create was handed %s", when.Format(time.RFC3339))
	}
	if !stored.Equal(when) {
		t.Errorf("completed_at = %s, want %s", stored.UTC().Format(time.RFC3339), when.Format(time.RFC3339))
	}
	// The RETURNED issue must agree with the column, or a caller reading the response sees a
	// different truth from a caller reading the database.
	if got.CompletedAt == nil || !got.CompletedAt.Equal(when) {
		t.Errorf("returned CompletedAt = %v, want %s", got.CompletedAt, when.Format(time.RFC3339))
	}
}

// ...and a NON-done issue does not, whatever the caller passes. This is the half that keeps the
// change from opening a hole: without it, adding the column to the INSERT lets a request body put a
// completion time on backlog work, which every analytics query counts as delivered.
func TestCreate_RefusesACompletionTimeOnANonDoneIssue(t *testing.T) {
	d := testutil.New(t)
	s := issue.NewStore(d.Pool)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	when := time.Date(2025, 3, 25, 10, 3, 0, 0, time.UTC)
	for _, status := range []model.IssueStatus{
		model.StatusBacklog, model.StatusTodo, model.StatusInProgress,
		model.StatusInReview, model.StatusCancelled,
	} {
		got := createIssue(t, s, ws.ID, team.ID, model.Issue{
			Title: "Not finished " + string(status), Status: status, CompletedAt: &when,
		})
		if stored := readCompletedAt(t, d, got.ID); stored != nil {
			t.Errorf("status %q: completed_at = %s in Postgres, want NULL — Update clears it on every "+
				"transition away from done, so no Track path can produce this row",
				status, stored.UTC().Format(time.RFC3339))
		}
		if got.CompletedAt != nil {
			t.Errorf("status %q: returned CompletedAt = %v, want nil", status, got.CompletedAt)
		}
	}
}

// The ordinary case, asserted so the change cannot pass by writing something always: a done issue
// created with NO completion time gets a NULL, not a now().
func TestCreate_ADoneIssueWithNoCompletionTimeStaysNull(t *testing.T) {
	d := testutil.New(t)
	s := issue.NewStore(d.Pool)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	got := createIssue(t, s, ws.ID, team.ID, model.Issue{Title: "Shipped, untimed", Status: model.StatusDone})
	if stored := readCompletedAt(t, d, got.ID); stored != nil {
		t.Errorf("completed_at = %s, want NULL — Create was handed none", stored.UTC().Format(time.RFC3339))
	}
}

// And Update's existing stamping still owns the lifecycle: creating a done issue with a completion
// time and then moving it AWAY from done must clear it, exactly as it does for any other row. This
// pins that the new INSERT column did not create a row Update cannot manage.
func TestCreate_ThenTransitionAwayFromDone_ClearsTheCompletionTime(t *testing.T) {
	d := testutil.New(t)
	s := issue.NewStore(d.Pool)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	when := time.Date(2025, 3, 25, 10, 3, 0, 0, time.UTC)
	got := createIssue(t, s, ws.ID, team.ID, model.Issue{
		Title: "Reopened later", Status: model.StatusDone, CompletedAt: &when,
	})
	if readCompletedAt(t, d, got.ID) == nil {
		t.Fatalf("precondition failed: completed_at is NULL right after Create")
	}
	if _, err := s.Update(context.Background(), got.ID, ws.ID,
		map[string]any{"status": string(model.StatusInProgress)}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if stored := readCompletedAt(t, d, got.ID); stored != nil {
		t.Errorf("after moving off done, completed_at = %s, want NULL", stored.UTC().Format(time.RFC3339))
	}
}
