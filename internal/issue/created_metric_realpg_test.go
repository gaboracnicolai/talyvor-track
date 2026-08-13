package issue_test

// created_metric_realpg_test.go — the two doors every issue in this product is written through,
// and the counter that claims to total them.
//
// FOUND FROM THE IMPORT SIDE (W3.4) and pinned HERE, at the layer the increment lives on, because
// the defect was that it lived somewhere else: `metrics.IssuesCreated` had ONE production call
// site, internal/issue/handler.go's POST /v1/issues, while FIVE production paths create an issue
// (that handler, the importer's two branches, the MCP tool surface, the automation engine). Four
// of the five moved no counter. The end-to-end measurement is in
// internal/importer/created_metric_job_test.go: `succeeded imported=2`, two rows in the table,
// counter zero.
//
// ⚠ WHY THE DOORS ARE TWO AND NOT ONE, AND WHY BOTH ARE ASSERTED SEPARATELY: an import with a
// provider key goes through UpsertByIdentifier and an import without one goes through Create, so a
// fix that counted only the INSERT would leave the entire keyed population — every Jira CSV with an
// `Issue key` column, every Linear CSV with an `ID` column, both API transports — uncounted, and
// the end-to-end test above would still be green because its fixture happens to be keyed.
//
// ⚠ AND THE UPDATE BRANCH IS ASSERTED TO COUNT NOTHING. An upsert that overwrites an existing issue
// created no issue; counting it would turn a re-import of a 5,000-row export into 5,000 phantom
// creations, which is a worse number than the zero this replaces because it looks like work.

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/model"
	tt "github.com/talyvor/track/internal/testutil"
)

// createdCount reads the ONE series a write into (ws, team, status) increments. Read as a delta by
// every caller: the counter is a process-global and this binary's other tests write issues too.
func createdCount(ws, team string, status model.IssueStatus) float64 {
	return testutil.ToFloat64(metrics.IssuesCreated.WithLabelValues(ws, team, string(status)))
}

func TestStore_Create_CountsTheIssueItCreated(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	before := createdCount(ws.ID, team.ID, model.StatusTodo)
	out, err := s.Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, Title: "counted", CreatorID: "u1", Status: model.StatusTodo,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if out.Status != model.StatusTodo {
		t.Fatalf("PREMISE FAILED: stored status %q, want %q — the series read below is the wrong one",
			out.Status, model.StatusTodo)
	}
	if got := createdCount(ws.ID, team.ID, model.StatusTodo) - before; got != 1 {
		t.Errorf("track_issues_created_total moved by %v, want 1 — a row is in the issues table and "+
			"the counter that claims to total issue creation did not see it", got)
	}
}

// refuseIssueInsert makes the issues INSERT itself fail, which is the only way to reach
// countCreated with a write that did not happen.
//
// ⚠⚠ THE FIRST VERSION OF THIS TEST USED A CROSS-WORKSPACE TEAM AND WAS VACUOUS. Create refuses
// that at its TEAM LOOKUP — twenty lines and four early returns before the INSERT — so countCreated
// was never called and the test could not tell a guarded counter from an unguarded one. MEASURED,
// not reasoned: control C4a deletes countCreated's guard ENTIRELY and that version scored NOT
// CAUGHT. Every pre-insert refusal (a bad ref, a team in another workspace, an allocator error)
// has that shape; the statement itself has to be the thing that fails.
const refuseIssueInsert = `
CREATE OR REPLACE FUNCTION refuse_issue_insert() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'issue insert refused (test fixture)';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER refuse_issue_insert BEFORE INSERT ON issues
  FOR EACH ROW EXECUTE FUNCTION refuse_issue_insert();
`

func TestStore_AFailedCreateCountsNothing(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	if _, err := d.Pool.Exec(ctx, refuseIssueInsert); err != nil {
		t.Fatalf("install refusal trigger: %v", err)
	}

	// The must-stay-zero companion. Without it, "the counter moved" is satisfied by an increment
	// that fires before the write lands, and an overcount reads as a fix.
	before := createdCount(ws.ID, team.ID, model.StatusTodo)
	if _, err := s.Create(ctx, model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, Title: "refused", CreatorID: "u1", Status: model.StatusTodo,
	}); err == nil {
		t.Fatalf("PREMISE FAILED: the INSERT was accepted with the refusal trigger installed — " +
			"this test is measuring the wrong state")
	}
	var rows int
	if err := d.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM issues WHERE workspace_id=$1`, ws.ID).Scan(&rows); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if rows != 0 {
		t.Fatalf("PREMISE FAILED: %d rows in issues after a refused INSERT, want 0", rows)
	}
	if got := createdCount(ws.ID, team.ID, model.StatusTodo) - before; got != 0 {
		t.Errorf("track_issues_created_total moved by %v on a write the database REFUSED, want 0 — "+
			"the issues table is empty and the counter says an issue was created", got)
	}
}

func TestStore_UpsertByIdentifier_CountsAnInsertAndNotAnUpdate(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	row := model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, Title: "ENG-1 first",
		CreatorID: model.ImporterCreatorID, Identifier: "ENG-1", Status: model.StatusTodo,
	}

	before := createdCount(ws.ID, team.ID, model.StatusTodo)
	if _, inserted, err := s.UpsertByIdentifier(ctx, row); err != nil || !inserted {
		t.Fatalf("PREMISE FAILED: first upsert inserted=%v err=%v, want inserted/nil", inserted, err)
	}
	if got := createdCount(ws.ID, team.ID, model.StatusTodo) - before; got != 1 {
		t.Errorf("an upsert that INSERTED moved the counter by %v, want 1 — this is the branch every "+
			"keyed import row takes (a Jira `Issue key`, a Linear `ID`, both API transports)", got)
	}

	// The SAME key again: the statement takes its UPDATE branch and creates no issue.
	afterInsert := createdCount(ws.ID, team.ID, model.StatusTodo)
	row.Title = "ENG-1 second"
	if _, inserted, err := s.UpsertByIdentifier(ctx, row); err != nil || inserted {
		t.Fatalf("PREMISE FAILED: second upsert inserted=%v err=%v, want NOT inserted/nil", inserted, err)
	}
	if got := createdCount(ws.ID, team.ID, model.StatusTodo) - afterInsert; got != 0 {
		t.Errorf("an upsert that UPDATED moved the counter by %v, want 0 — a re-import creates no "+
			"issues, and counting it would report a 5,000-row re-import as 5,000 creations", got)
	}
}
