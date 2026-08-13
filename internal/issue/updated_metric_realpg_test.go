package issue_test

// updated_metric_realpg_test.go — the three doors an issue in this product is UPDATED through, and
// the counter whose Help says "Total number of issue updates".
//
// ⚠⚠ THE SAME DEFECT AS track_issues_created_total, ONE METRIC OVER, AND IT WAS HANDED ON RATHER
// THAN GUESSED. `metrics.IssuesUpdated` had exactly ONE production call site —
// internal/issue/handler.go's PATCH /v1/issues/{id} — against FIFTEEN production paths that update
// an issue: that route, TWELVE other Store.Update callers (internal/mcp/server.go ×3,
// internal/automation/engine.go ×7, internal/automation/github.go ×1, internal/ai/handler.go ×1),
// the bulk route (Store.BulkUpdate, which powers kanban drag-and-drop), and the importer's
// upsert-UPDATE branch (every RE-import of a keyed export). FOURTEEN of the fifteen moved nothing.
//
// ⚠ WHY THE DOORS ARE THREE AND NOT ONE. Update, BulkUpdate and UpsertByIdentifier's conflict arm
// are three separate UPDATE statements on the issues table, and no two of them share a code path:
// a fix that counted only Store.Update would leave the entire kanban board and every re-import
// uncounted, and a per-door test that exercised only Update would be green while it did.
//
// ⚠ AND THE MUST-STAY-ZEROS ARE HALF THIS FILE, because the failure mode of a fix here is an
// OVERCOUNT, which looks like work. An Update whose field map contains nothing updatable writes no
// row (it returns the issue via a plain SELECT); a by-id update scoped to another workspace matches
// nothing; a statement the database refuses wrote nothing; and a bulk item naming a foreign id is
// skipped by design. Each of those must move the counter by exactly 0.

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/metrics"
	"github.com/talyvor/track/internal/model"
	tt "github.com/talyvor/track/internal/testutil"
)

// updatedCount reads the ONE series an update into (ws, team, resulting status) increments. Read as
// a delta by every caller: the counter is a process-global and this binary's other tests update
// issues too.
func updatedCount(ws, team string, status model.IssueStatus) float64 {
	return testutil.ToFloat64(metrics.IssuesUpdated.WithLabelValues(ws, team, string(status)))
}

// seedIssue writes one issue through the ordinary door and fails the test if it did not land — the
// tests below assert DELTAS around an update, so a create that silently did nothing would make
// every one of them read zero for the wrong reason.
func seedIssue(t *testing.T, s *issue.Store, ws, team, title string, status model.IssueStatus) *model.Issue {
	t.Helper()
	out, err := s.Create(context.Background(), model.Issue{
		WorkspaceID: ws, TeamID: team, Title: title, CreatorID: "u1", Status: status,
	})
	if err != nil {
		t.Fatalf("PREMISE FAILED: seed create: %v", err)
	}
	return out
}

func TestStore_Update_CountsTheIssueItUpdated(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	iss := seedIssue(t, s, ws.ID, team.ID, "to be updated", model.StatusTodo)

	before := updatedCount(ws.ID, team.ID, model.StatusInProgress)
	beforeOld := updatedCount(ws.ID, team.ID, model.StatusTodo)
	out, err := s.Update(ctx, iss.ID, ws.ID, map[string]any{"status": string(model.StatusInProgress)})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if out.Status != model.StatusInProgress {
		t.Fatalf("PREMISE FAILED: stored status %q, want %q — the series read below is the wrong one",
			out.Status, model.StatusInProgress)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusInProgress) - before; got != 1 {
		t.Errorf("track_issues_updated_total moved by %v, want 1 — this is the door TWELVE production "+
			"callers use (MCP ×3, automation engine ×7, automation/github ×1, ai/handler ×1) and the "+
			"counter that claims to total issue updates did not see any of them", got)
	}
	// The label is the NEW status. A fix that read the status off the pre-update row would still
	// move "the counter" and would file every transition under the state it came from.
	if got := updatedCount(ws.ID, team.ID, model.StatusTodo) - beforeOld; got != 0 {
		t.Errorf("the OLD status series moved by %v, want 0 — the Help says \"labelled by ... new "+
			"status\", so a todo→in_progress transition belongs to in_progress alone", got)
	}
}

// An update whose field map survives the allowlist empty runs NO statement — Update returns the row
// via getInWorkspace, a plain SELECT. Counting it reports edits that never happened, and the shape
// is not exotic: `updatableFields` silently drops any key it does not name, which is exactly how
// milestone_id went unwritable for a release.
func TestStore_AnUpdateThatWroteNothingCountsNothing(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	iss := seedIssue(t, s, ws.ID, team.ID, "untouched", model.StatusTodo)
	var updatedAtBefore string
	if err := d.Pool.QueryRow(ctx, `SELECT updated_at::text FROM issues WHERE id=$1`, iss.ID).Scan(&updatedAtBefore); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}

	before := updatedCount(ws.ID, team.ID, model.StatusTodo)
	// "identifier" is not in updatableFields, so every set clause is dropped.
	if _, err := s.Update(ctx, iss.ID, ws.ID, map[string]any{"identifier": "NOPE-1"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	var updatedAtAfter string
	if err := d.Pool.QueryRow(ctx, `SELECT updated_at::text FROM issues WHERE id=$1`, iss.ID).Scan(&updatedAtAfter); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if updatedAtAfter != updatedAtBefore {
		t.Fatalf("PREMISE FAILED: updated_at moved (%s → %s) — this call DID write, so it is the wrong "+
			"instrument for a must-stay-zero", updatedAtBefore, updatedAtAfter)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusTodo) - before; got != 0 {
		t.Errorf("track_issues_updated_total moved by %v on a call that ran no UPDATE statement, want 0 "+
			"— updated_at is byte-identical before and after and the counter says the issue was edited", got)
	}
}

// SEC-5: a by-id update scoped to another workspace matches no row and returns ErrNotFound. The
// counter must not record an edit that the tenancy predicate refused.
func TestStore_AnUpdateInAnotherWorkspaceCountsNothing(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	other := d.Workspace(t)
	s := issue.NewStore(d.Pool)

	iss := seedIssue(t, s, ws.ID, team.ID, "mine", model.StatusTodo)

	before := updatedCount(ws.ID, team.ID, model.StatusInProgress)
	beforeOther := updatedCount(other.ID, team.ID, model.StatusInProgress)
	_, err := s.Update(ctx, iss.ID, other.ID, map[string]any{"status": string(model.StatusInProgress)})
	if err == nil {
		t.Fatalf("PREMISE FAILED: a cross-workspace update SUCCEEDED — this test is measuring the wrong state")
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusInProgress) - before; got != 0 {
		t.Errorf("track_issues_updated_total moved by %v for the OWNING workspace on a refused "+
			"cross-tenant update, want 0", got)
	}
	if got := updatedCount(other.ID, team.ID, model.StatusInProgress) - beforeOther; got != 0 {
		t.Errorf("track_issues_updated_total moved by %v for the CALLING workspace on a refused "+
			"cross-tenant update, want 0", got)
	}
}

// refuseIssueUpdate makes the issues UPDATE itself fail. It is the only shape that reaches the
// counting site with a write that did not happen: every EARLIER refusal (a bad ref, a foreign
// workspace, an empty allowlist) returns before the statement runs, so a test built on one of those
// cannot tell a guarded counter from an unguarded one. That mistake was made once already on the
// created-metric side and was caught only by a control — see created_metric_realpg_test.go.
const refuseIssueUpdate = `
CREATE OR REPLACE FUNCTION refuse_issue_update() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'issue update refused (test fixture)';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER refuse_issue_update BEFORE UPDATE ON issues
  FOR EACH ROW EXECUTE FUNCTION refuse_issue_update();
`

func TestStore_AFailedUpdateCountsNothing(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	iss := seedIssue(t, s, ws.ID, team.ID, "refused", model.StatusTodo)
	if _, err := d.Pool.Exec(ctx, refuseIssueUpdate); err != nil {
		t.Fatalf("install refusal trigger: %v", err)
	}

	before := updatedCount(ws.ID, team.ID, model.StatusInProgress)
	if _, err := s.Update(ctx, iss.ID, ws.ID, map[string]any{"status": string(model.StatusInProgress)}); err == nil {
		t.Fatalf("PREMISE FAILED: the UPDATE was accepted with the refusal trigger installed — " +
			"this test is measuring the wrong state")
	}
	var status string
	if err := d.Pool.QueryRow(ctx, `SELECT status FROM issues WHERE id=$1`, iss.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != string(model.StatusTodo) {
		t.Fatalf("PREMISE FAILED: status is %q after a refused UPDATE, want %q", status, model.StatusTodo)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusInProgress) - before; got != 0 {
		t.Errorf("track_issues_updated_total moved by %v on a write the database REFUSED, want 0 — "+
			"the row still carries its old status and the counter says it was edited", got)
	}
}

// The importer's door. A re-import of a keyed export takes the conflict arm for every row it
// touches; the statement's own `RETURNING (xmax = 0)` already tells the store which branch ran, so
// this is the one place where "created" and "updated" are distinguishable without a second query.
func TestStore_UpsertByIdentifier_CountsAnUpdateAndNotAnInsert(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	row := model.Issue{
		WorkspaceID: ws.ID, TeamID: team.ID, Title: "ENG-1 first",
		CreatorID: model.ImporterCreatorID, Identifier: "ENG-1", Status: model.StatusTodo,
	}

	// An INSERT created an issue and updated none.
	before := updatedCount(ws.ID, team.ID, model.StatusTodo)
	if _, inserted, err := s.UpsertByIdentifier(ctx, row); err != nil || !inserted {
		t.Fatalf("PREMISE FAILED: first upsert inserted=%v err=%v, want inserted/nil", inserted, err)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusTodo) - before; got != 0 {
		t.Errorf("an upsert that INSERTED moved track_issues_updated_total by %v, want 0 — a first "+
			"import of 5,000 rows updated nothing, and counting it would double every import in the "+
			"two counters at once", got)
	}

	// The SAME key again: the conflict arm overwrites an existing issue.
	afterInsert := updatedCount(ws.ID, team.ID, model.StatusTodo)
	beforeCreated := createdCount(ws.ID, team.ID, model.StatusTodo)
	row.Title = "ENG-1 second"
	if _, inserted, err := s.UpsertByIdentifier(ctx, row); err != nil || inserted {
		t.Fatalf("PREMISE FAILED: second upsert inserted=%v err=%v, want NOT inserted/nil", inserted, err)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusTodo) - afterInsert; got != 1 {
		t.Errorf("an upsert that UPDATED moved track_issues_updated_total by %v, want 1 — this is "+
			"every RE-import of a keyed export (a Jira `Issue key`, a Linear `ID`, both API "+
			"transports), the path that rewrites thousands of rows at a time", got)
	}
	if got := createdCount(ws.ID, team.ID, model.StatusTodo) - beforeCreated; got != 0 {
		t.Errorf("the same upsert moved track_issues_created_total by %v, want 0 — the two counters "+
			"must not both claim one statement", got)
	}
}

// The kanban door. BulkUpdate is one transaction of many single-row statements, so the count it
// returns and the count the metric records have to be the same number.
func TestStore_BulkUpdate_CountsEveryRowItUpdated(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	a := seedIssue(t, s, ws.ID, team.ID, "card A", model.StatusTodo)
	b := seedIssue(t, s, ws.ID, team.ID, "card B", model.StatusTodo)

	before := updatedCount(ws.ID, team.ID, model.StatusDone)
	n, err := s.BulkUpdate(ctx, ws.ID, []issue.BulkUpdateItem{
		{ID: a.ID, Status: string(model.StatusDone)},
		{ID: b.ID, Status: string(model.StatusDone)},
	})
	if err != nil {
		t.Fatalf("bulk update: %v", err)
	}
	if n != 2 {
		t.Fatalf("PREMISE FAILED: BulkUpdate reported %d rows, want 2 — the counter assertion below "+
			"would be comparing against an update that did not happen", n)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusDone) - before; got != 2 {
		t.Errorf("track_issues_updated_total moved by %v, want 2 — the bulk route reported 2 rows "+
			"updated and the counter saw %v of them. This is the kanban board: every card drag in "+
			"the product goes through here.", got, got)
	}
}

// The commonest kanban action is a drag WITHIN a column: sort_order moves and status does not. The
// bulk item then carries NO status at all, so the label cannot come from the request — it can only
// come from the row, which is why the per-item statement returns it. Reading it off the item would
// file every within-column drag under the empty-string series, and an empty label is a series that
// exists, is exported, and means nothing.
func TestStore_BulkUpdate_ADragWithinAColumnCountsTheStatusTheRowAlreadyHad(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	card := seedIssue(t, s, ws.ID, team.ID, "dragged within a column", model.StatusInReview)

	before := updatedCount(ws.ID, team.ID, model.StatusInReview)
	beforeEmpty := testutil.ToFloat64(metrics.IssuesUpdated.WithLabelValues(ws.ID, team.ID, ""))
	n, err := s.BulkUpdate(ctx, ws.ID, []issue.BulkUpdateItem{{ID: card.ID, SortOrder: 3.5}})
	if err != nil {
		t.Fatalf("bulk update: %v", err)
	}
	if n != 1 {
		t.Fatalf("PREMISE FAILED: BulkUpdate reported %d rows, want 1", n)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusInReview) - before; got != 1 {
		t.Errorf("track_issues_updated_total moved by %v in the row's own status series, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.IssuesUpdated.WithLabelValues(ws.ID, team.ID, "")) - beforeEmpty; got != 0 {
		t.Errorf("the EMPTY status series moved by %v, want 0 — the drag set no status, and the "+
			"label was read off the request instead of off the row", got)
	}
}

// ⚠⚠ THE ONE UPDATE DOOR THAT CAN WRITE AND THEN UN-WRITE. BulkUpdate is a single transaction: a
// mid-batch statement error returns before Commit and the deferred Rollback undoes every row the
// loop had already written. A counter incremented per statement would record edits the database
// threw away, and a Prometheus counter cannot be decremented — the operator's number would drift
// upward for the life of the process, one failed drag at a time.
//
// The failure is driven by a trigger that refuses ONE row by id, so the batch's FIRST statement
// genuinely succeeds and is genuinely rolled back. A test that failed on the first item would be
// green against a per-statement increment and would prove nothing.
const doomedIssueTitle = "refuse this one"

const refuseUpdateOfOneIssue = `
CREATE OR REPLACE FUNCTION refuse_one_issue_update() RETURNS trigger AS $$
BEGIN
  IF NEW.title = '` + doomedIssueTitle + `' THEN
    RAISE EXCEPTION 'issue update refused (test fixture)';
  END IF;
  RETURN NEW;
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER refuse_one_issue_update BEFORE UPDATE ON issues
  FOR EACH ROW EXECUTE FUNCTION refuse_one_issue_update();
`

func TestStore_BulkUpdate_ABatchThatRolledBackCountsNothing(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	s := issue.NewStore(d.Pool)

	good := seedIssue(t, s, ws.ID, team.ID, "will be rolled back", model.StatusTodo)
	doomed := seedIssue(t, s, ws.ID, team.ID, doomedIssueTitle, model.StatusTodo)

	if _, err := d.Pool.Exec(ctx, refuseUpdateOfOneIssue); err != nil {
		t.Fatalf("install refusal trigger: %v", err)
	}

	before := updatedCount(ws.ID, team.ID, model.StatusDone)
	n, err := s.BulkUpdate(ctx, ws.ID, []issue.BulkUpdateItem{
		{ID: good.ID, Status: string(model.StatusDone)},
		{ID: doomed.ID, Status: string(model.StatusDone)},
	})
	if err == nil {
		t.Fatalf("PREMISE FAILED: the batch SUCCEEDED (n=%d) with the refusal trigger installed — "+
			"this test is measuring the wrong state", n)
	}
	var status string
	if err := d.Pool.QueryRow(ctx, `SELECT status FROM issues WHERE id=$1`, good.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != string(model.StatusTodo) {
		t.Fatalf("PREMISE FAILED: the first item's status is %q, want %q — the transaction did NOT "+
			"roll back, so there is no rolled-back write to assert about", status, model.StatusTodo)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusDone) - before; got != 0 {
		t.Errorf("track_issues_updated_total moved by %v for a transaction the database ROLLED BACK, "+
			"want 0 — the row still carries its old status and the counter, which cannot be "+
			"decremented, says it was edited", got)
	}
}

// A bulk item naming another workspace's issue matches 0 rows BY DESIGN (the per-item WHERE carries
// AND workspace_id) and is excluded from the returned count. The counter must exclude it too — this
// is the one shape where an overcount would also be a cross-tenant disclosure, since the label
// carries the caller's workspace and the row belongs to someone else.
func TestStore_BulkUpdate_ARowThatMatchedNothingCountsNothing(t *testing.T) {
	d := tt.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	other := d.Workspace(t)
	otherTeam := d.Team(t, other.ID)
	s := issue.NewStore(d.Pool)

	mine := seedIssue(t, s, ws.ID, team.ID, "mine", model.StatusTodo)
	theirs := seedIssue(t, s, other.ID, otherTeam.ID, "theirs", model.StatusTodo)

	before := updatedCount(ws.ID, team.ID, model.StatusDone)
	beforeTheirs := updatedCount(other.ID, otherTeam.ID, model.StatusDone)
	n, err := s.BulkUpdate(ctx, ws.ID, []issue.BulkUpdateItem{
		{ID: mine.ID, Status: string(model.StatusDone)},
		{ID: theirs.ID, Status: string(model.StatusDone)},
	})
	if err != nil {
		t.Fatalf("bulk update: %v", err)
	}
	if n != 1 {
		t.Fatalf("PREMISE FAILED: BulkUpdate reported %d rows, want 1 — the foreign id must match "+
			"nothing, and if it matched, the tenancy predicate is the finding, not the counter", n)
	}
	if got := updatedCount(ws.ID, team.ID, model.StatusDone) - before; got != 1 {
		t.Errorf("track_issues_updated_total moved by %v for the caller's workspace, want 1", got)
	}
	if got := updatedCount(other.ID, otherTeam.ID, model.StatusDone) - beforeTheirs; got != 0 {
		t.Errorf("track_issues_updated_total moved by %v for the OTHER workspace, want 0 — the write "+
			"was refused by the tenancy predicate and the counter announced it anyway", got)
	}
}
