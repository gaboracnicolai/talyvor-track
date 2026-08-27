package automation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// THE FINDING, MEASURED ON REAL POSTGRES RATHER THAN READ.
//
// Engine.Fire accumulates `actionsTaken` by appending only on SUCCESS, so a rule whose
// actions ALL fail leaves it nil. logRun binds that nil straight into
// `actions_taken TEXT[] NOT NULL` (migration 0008), and a parameter explicitly bound to
// NULL does NOT fall back to the column DEFAULT '{}' — Postgres refuses the row with
// SQLSTATE 23502. logRun swallows that into a slog.Warn and Fire returns nil.
//
// So automation_logs — the table GET /v1/workspaces/{wsID}/automation/logs exists to
// serve, "so operators can audit what fired and when" (handler.go) — is EMPTY EXACTLY
// WHEN THE AUTOMATION FAILED COMPLETELY, and populated when it only failed partly. The
// worse the failure, the less of it is recorded.
//
// Measured before the fix, all three via ordinary misconfiguration, none exotic:
//
//	notify_slack with no sender configured (the DEFAULT wiring) -> logs 0 -> 0
//	create_issue with no title in action_data                   -> logs 0 -> 0
//	set_priority with a non-integer value                       -> logs 0 -> 0
//	close_issue (ok) + notify_slack (fails)  [PARTIAL]          -> logs 0 -> 1
//
// with `ERROR: null value in column "actions_taken" of relation "automation_logs"
// violates not-null constraint (SQLSTATE 23502)` on stderr for each of the first three.
//
// Nothing was watching: before this file, `automation_logs`, `logRun` and `ListLogs`
// had ZERO assertions anywhere in the repository.

// auditRow is one automation_logs row, read back the way ListLogs reads it.
type auditRow struct {
	trigger      string
	actionsTaken []string
	actionsNull  bool
	success      bool
	errStr       string
}

func readAuditRows(t *testing.T, d *testutil.DB, ruleID string) []auditRow {
	t.Helper()
	rows, err := d.Pool.Query(context.Background(),
		`SELECT trigger, actions_taken, actions_taken IS NULL, success, error
         FROM automation_logs WHERE rule_id = $1 ORDER BY created_at`, ruleID)
	if err != nil {
		t.Fatalf("read automation_logs: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.trigger, &r.actionsTaken, &r.actionsNull, &r.success, &r.errStr); err != nil {
			t.Fatalf("scan automation_logs: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// fixture builds a workspace + team + issue and an engine with NO slack sender —
// which is the default wiring and the cheapest real way to make an action fail.
func auditFixture(t *testing.T) (*testutil.DB, *Engine, string, string, model.Issue) {
	t.Helper()
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	iss := d.Issue(t, ws.ID, team.ID)
	e := newEngine(d.Pool, issue.NewStore(d.Pool), nil)
	return d, e, ws.ID, team.ID, *iss
}

func addRule(t *testing.T, e *Engine, wsID, teamID, name string, actions []RuleAction, data map[string]string) string {
	t.Helper()
	out, err := e.AddRule(context.Background(), Rule{
		WorkspaceID: wsID, TeamID: teamID, Name: name,
		Trigger: TriggerIssueCreated, Actions: actions, ActionData: data,
	})
	if err != nil {
		t.Fatalf("AddRule(%s): %v", name, err)
	}
	return out.ID
}

// TestFire_WhenEveryActionFails_TheRunIsStillAudited_RealPG is the finding: a run in
// which nothing succeeded must still leave an audit row, or the log is silent exactly
// when an operator most needs it.
func TestFire_WhenEveryActionFails_TheRunIsStillAudited_RealPG(t *testing.T) {
	d, e, wsID, teamID, iss := auditFixture(t)
	ruleID := addRule(t, e, wsID, teamID, "slack, unconfigured", []RuleAction{ActionNotifySlack}, nil)

	if err := e.Fire(context.Background(), TriggerIssueCreated, wsID, iss, nil); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	rows := readAuditRows(t, d, ruleID)
	if len(rows) != 1 {
		t.Fatalf("automation_logs rows for a totally-failed run = %d, want 1.\n"+
			"A rule whose every action failed left NO trace in the table that "+
			"GET /automation/logs serves — the operator sees the same empty log as a "+
			"rule that never triggered at all.", len(rows))
	}
	got := rows[0]
	if got.actionsNull {
		t.Errorf("actions_taken IS NULL; the column is TEXT[] NOT NULL, so this row could not have been written")
	}
	if len(got.actionsTaken) != 0 {
		t.Errorf("actions_taken = %v, want empty (nothing ran)", got.actionsTaken)
	}
	if got.success {
		t.Errorf("success = true for a run in which every action failed")
	}
	if got.errStr == "" {
		t.Errorf("error is empty; the run failed and the reason must be recorded")
	}
	if !strings.Contains(got.errStr, "slack") {
		t.Errorf("error = %q, want it to name the failing action's cause", got.errStr)
	}
	if got.trigger != string(TriggerIssueCreated) {
		t.Errorf("trigger = %q, want %q", got.trigger, TriggerIssueCreated)
	}
}

// TestFire_PartialFailure_StillRecordsTheActionsThatRan_RealPG is the OTHER DIRECTION.
// "Always write an empty array" would satisfy the test above and destroy the log's only
// useful content, so the actions that DID run are pinned by name.
func TestFire_PartialFailure_StillRecordsTheActionsThatRan_RealPG(t *testing.T) {
	d, e, wsID, teamID, iss := auditFixture(t)
	ruleID := addRule(t, e, wsID, teamID, "one ok, one broken",
		[]RuleAction{ActionCloseIssue, ActionNotifySlack}, nil)

	if err := e.Fire(context.Background(), TriggerIssueCreated, wsID, iss, nil); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	rows := readAuditRows(t, d, ruleID)
	if len(rows) != 1 {
		t.Fatalf("automation_logs rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if len(got.actionsTaken) != 1 || got.actionsTaken[0] != string(ActionCloseIssue) {
		t.Errorf("actions_taken = %v, want exactly [%s] — the action that succeeded must still be named",
			got.actionsTaken, ActionCloseIssue)
	}
	if got.success {
		t.Errorf("success = true, but one action failed")
	}
	if got.errStr == "" {
		t.Errorf("error is empty, but one action failed")
	}
}

// TestFire_AllActionsSucceed_RecordsThemAndSucceeds_RealPG is the happy path, pinned so
// the fix cannot buy the failure case by breaking the ordinary one.
func TestFire_AllActionsSucceed_RecordsThemAndSucceeds_RealPG(t *testing.T) {
	d, e, wsID, teamID, iss := auditFixture(t)
	ruleID := addRule(t, e, wsID, teamID, "works", []RuleAction{ActionCloseIssue}, nil)

	if err := e.Fire(context.Background(), TriggerIssueCreated, wsID, iss, nil); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	rows := readAuditRows(t, d, ruleID)
	if len(rows) != 1 {
		t.Fatalf("automation_logs rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if len(got.actionsTaken) != 1 || got.actionsTaken[0] != string(ActionCloseIssue) {
		t.Errorf("actions_taken = %v, want [%s]", got.actionsTaken, ActionCloseIssue)
	}
	if !got.success {
		t.Errorf("success = false on a run where every action succeeded (error=%q)", got.errStr)
	}
	if got.errStr != "" {
		t.Errorf("error = %q, want empty", got.errStr)
	}
}

// TestListLogs_ShowsATotallyFailedRun_RealPG makes the claim the PRODUCT one: the
// finding is not that a row is missing from a table, it is that the endpoint an operator
// reads shows nothing. Driven through the real handler with a real authorized context.
func TestListLogs_ShowsATotallyFailedRun_RealPG(t *testing.T) {
	_, e, wsID, teamID, iss := auditFixture(t)
	_ = addRule(t, e, wsID, teamID, "slack, unconfigured", []RuleAction{ActionNotifySlack}, nil)

	if err := e.Fire(context.Background(), TriggerIssueCreated, wsID, iss, nil); err != nil {
		t.Fatalf("Fire: %v", err)
	}

	h := NewHandler(e)
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+wsID+"/automation/logs", nil)
	req = req.WithContext(authz.WithAuthorized(req.Context(), wsID, "member-1"))
	rec := httptest.NewRecorder()
	h.ListLogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ListLogs status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ListLogs body: %v (raw %s)", err, rec.Body.String())
	}
	if len(body) != 1 {
		t.Fatalf("GET /automation/logs returned %d rows, want 1.\n"+
			"An operator whose rule is completely broken reads an EMPTY log — "+
			"indistinguishable from a rule that never fired.\nbody: %s", len(body), rec.Body.String())
	}
}
