package issue_test

import (
	"context"
	"testing"
	"time"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// create_created_at_test.go — Create's INSERT does not name `created_at`, and the column has
// `DEFAULT NOW()`.
//
// FOUND FROM THE IMPORT SIDE (W3.4) and pinned here, next to the statement, for the same reason
// create_completed_at_test.go is: it is a store defect. It is the THIRD copy of the seam #74 found
// in the importer's UPSERT and #78 found in this INSERT for the neighbouring column — and it is the
// worst of the three, because the other two produced a NULL that anybody could spot. A defaulted
// created_at produces a plausible timestamp. The loss is invisible in the row and shows up only as
// a NEGATIVE time to resolution in analytics (measured through the async runner on real Postgres:
// -2400.0 hours for an issue Jira opened 200 days before the import).
//
// ⚠ THE GATE IS THE LOAD-BEARING HALF AND THESE TESTS ARE WRITTEN AROUND IT. CreatedAt carries a
// `json:"created_at"` tag and handler.Create decodes the WHOLE model.Issue off the request body, so
// naming the column with no rule hands every authenticated client the WINDOW PREDICATE of every
// analytics report (`created_at > NOW() - INTERVAL '1 day' * $2`): work that no report can see, and
// any cycle time it cares to fabricate. The rule is that only a row the IMPORTER created may carry
// a supplied created_at — reachable because handler.Create refuses a supplied creator_id outright
// (SEC-5: `in.CreatorID = actorID`, never a body field).
//
// ⚠ THE NON-IMPORTER DIRECTION IS THE ONE THAT MATTERS AND IT IS THE ONE NOBODY WRITES. #82's C3:
// a new classifier is blind to its own inverse, because the acted-on cases are the natural tests. A
// gate that lets everything through passes every test below except TestCreate_RefusesACallerSupplied…

func readCreatedAt(t *testing.T, d *testutil.DB, issueID string) time.Time {
	t.Helper()
	var out time.Time
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT created_at FROM issues WHERE id = $1`, issueID).Scan(&out); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	return out.UTC()
}

func mustCreate(t *testing.T, s *issue.Store, i model.Issue) *model.Issue {
	t.Helper()
	got, err := s.Create(context.Background(), i)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return got
}

// An imported row keeps the instant the PROVIDER opened the issue.
func TestCreate_PersistsCreatedAtOnAnImportedIssue(t *testing.T) {
	d := testutil.New(t)
	s := issue.NewStore(d.Pool)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	opened := time.Date(2024, 2, 29, 8, 15, 0, 0, time.UTC)
	got := mustCreate(t, s, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, CreatorID: model.ImporterCreatorID,
		Title: "opened long ago", Status: model.StatusBacklog, CreatedAt: opened,
	})
	if col := readCreatedAt(t, d, got.ID); !col.Equal(opened) {
		t.Errorf("created_at column = %s, want %s — a mapper-only fix is discarded by this SQL",
			col.Format(time.RFC3339), opened.Format(time.RFC3339))
	}
	if !got.CreatedAt.UTC().Equal(opened) {
		t.Errorf("returned CreatedAt = %s, want %s", got.CreatedAt.UTC().Format(time.RFC3339), opened.Format(time.RFC3339))
	}
}

// ⚠ THE INVERSE. A caller that is NOT the importer may not choose its own created_at, however it
// asks. This is the assertion the whole gate exists for and the one a blinded gate passes nothing
// else on.
func TestCreate_RefusesACallerSuppliedCreatedAtOnANonImportedIssue(t *testing.T) {
	d := testutil.New(t)
	s := issue.NewStore(d.Pool)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	backdated := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Now().UTC().Add(-time.Minute)
	got := mustCreate(t, s, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, CreatorID: "member-42",
		Title: "filed by a client that would like to be invisible", Status: model.StatusBacklog,
		CreatedAt: backdated,
	})
	col := readCreatedAt(t, d, got.ID)
	if col.Equal(backdated) {
		t.Fatalf("created_at column = %s — a client just chose the window predicate of every "+
			"analytics report. created_at > NOW() - INTERVAL '1 day' * $2 filters every one of them.",
			col.Format(time.RFC3339))
	}
	if col.Before(before) {
		t.Errorf("created_at column = %s, want ≈ now — the gate must fall back to the DEFAULT, "+
			"not to some other supplied value", col.Format(time.RFC3339))
	}
}

// A zero CreatedAt is "nobody supplied one" and must take the DEFAULT rather than land as the zero
// time. Every native path leaves it zero — Create, the MCP server, feature-board conversion,
// automation — so this is the case almost every row in the product takes.
func TestCreate_AZeroCreatedAtTakesTheDatabaseDefault(t *testing.T) {
	d := testutil.New(t)
	s := issue.NewStore(d.Pool)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)

	before := time.Now().UTC().Add(-time.Minute)
	// The IMPORTER creator on purpose: it isolates the ZERO half of the rule from the CREATOR half,
	// so a gate that only checked the creator cannot pass this by writing the zero time.
	got := mustCreate(t, s, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, CreatorID: model.ImporterCreatorID,
		Title: "no creation time supplied", Status: model.StatusBacklog,
	})
	col := readCreatedAt(t, d, got.ID)
	if col.Before(before) {
		t.Errorf("created_at column = %s, want ≈ now — a zero CreatedAt reached the column",
			col.Format(time.RFC3339))
	}
}
