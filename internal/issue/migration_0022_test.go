package issue_test

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// (pre-flight) The 0022 duplicate guard must RAISE when a (workspace_id, identifier) maps to >1 issue — so a
// dirty deployment aborts LOUDLY before the ALTER, not opaquely. We drop the constraint (this DB is private
// to this test), seed a REAL duplicate in issues, then run the guard's byte-identical check.
func TestMigration0022_PreflightGuard_RaisesOnDuplicate(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	first, err := s.Create(ctx, model.Issue{WorkspaceID: ws.ID, TeamID: team.ID, Title: "x", CreatorID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Pool.Exec(ctx, `ALTER TABLE issues DROP CONSTRAINT issues_workspace_identifier_key`); err != nil {
		t.Fatalf("drop constraint (to seed a dup): %v", err)
	}
	// A second row with the SAME (workspace_id, identifier), a different number — copied from the real row so
	// every NOT NULL column is satisfied.
	if _, err := d.Pool.Exec(ctx,
		`INSERT INTO issues (workspace_id, team_id, number, identifier, title, creator_id)
		 SELECT workspace_id, team_id, 99999, identifier, title, creator_id FROM issues WHERE id=$1`,
		first.ID); err != nil {
		t.Fatalf("seed duplicate: %v", err)
	}

	_, err = d.Pool.Exec(ctx, `
		DO $$
		BEGIN
		    IF EXISTS (SELECT 1 FROM issues GROUP BY workspace_id, identifier HAVING count(*) > 1) THEN
		        RAISE EXCEPTION 'duplicate (workspace_id, identifier) rows exist in issues';
		    END IF;
		END $$;`)
	if err == nil {
		t.Fatal("0022 pre-flight guard must RAISE when a duplicate (workspace_id, identifier) exists")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("guard raised the wrong error: %v", err)
	}
}

// (positive) migration 0022 actually applied the named UNIQUE constraint — the ON CONFLICT target.
func TestMigration0022_ConstraintApplied(t *testing.T) {
	d := testutil.New(t)
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_constraint WHERE conname='issues_workspace_identifier_key' AND contype='u'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("issues_workspace_identifier_key UNIQUE constraint not found (got %d) — migration 0022 didn't apply", n)
	}
}
