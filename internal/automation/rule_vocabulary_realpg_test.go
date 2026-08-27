package automation

import (
	"context"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/testutil"
)

// THE FINDING, MEASURED THROUGH AddRule ON REAL POSTGRES RATHER THAN READ.
//
// An automation rule is built from FOUR vocabularies, and the engine can act on a closed
// set of each: 7 declared `RuleTrigger` constants, 9 declared `RuleAction` constants, the
// 4 condition fields `evaluateCondition` switches on, and the 4 operators `compare` /
// `compareLabels` switch on. AddRule validated the rule's SHAPE — trigger non-empty, at
// least one action, at most MaxActionsPerRule — and NONE of its vocabulary. Measured
// before the fix, every one of these was ACCEPTED with a 201 and persisted:
//
//	trigger "scheduled"      -> ACCEPTED   (a DECLARED constant that nothing fires)
//	trigger "totally.made.up"-> ACCEPTED   (undeclared entirely)
//	field   "assignee_id"    -> ACCEPTED   (the issue's JSON name; the engine wants "assignee")
//	operator "gt"            -> ACCEPTED   (there is no ordering operator at all)
//	action  "send_email"     -> ACCEPTED   (undeclared)
//
// and each then failed in a DIFFERENT way, which is the part that matters:
//
//   - an unknown ACTION reaches executeAction, errors "unknown action: …", and — since
//     ec76d96 — is recorded in automation_logs. LOUD and auditable.
//   - an unknown condition FIELD or OPERATOR falls off the end of a switch to `return
//     false`, so conditionsPass fails, Fire `continue`s BEFORE logRun, and the rule is
//     skipped leaving no row and no error. SILENT.
//   - an unknown TRIGGER matches no Fire call ever made. SILENT.
//
// So the half that decides WHETHER ANYTHING RUNS AT ALL was the silent half, and the
// only signal a user ever got was a 201.
//
// This is the same class talyvor-track already paid for one package over: b2f282e,
// "issues: the write path validates the KEYS of an update and none of the VALUES".
//
// ⚠ Note "scheduled": it is DECLARED and NOTHING FIRES IT — `git grep '\.Fire('` over
// non-test source returns six producers and none passes it. See
// TestTriggerVocabulary_TheDeclaredSetIsNotTheFirableSet below, which pins that gap so
// it cannot widen quietly.

func vocabFixture(t *testing.T) (*Engine, string, string) {
	t.Helper()
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	return newEngine(d.Pool, &fakeIssueUpdater{}, &fakeSlack{}), ws.ID, team.ID
}

func TestAddRule_RefusesVocabularyTheEngineCannotActOn_RealPG(t *testing.T) {
	e, wsID, teamID := vocabFixture(t)

	cases := []struct {
		name     string
		rule     Rule
		wantWord string // the refusal must NAME the offending value
	}{
		{"a trigger nothing fires (declared, no producer)", Rule{
			Trigger: TriggerScheduled, Actions: []RuleAction{ActionCloseIssue}}, "scheduled"},
		{"a trigger that is not declared at all", Rule{
			Trigger: RuleTrigger("totally.made.up"), Actions: []RuleAction{ActionCloseIssue}}, "totally.made.up"},
		{"an action the dispatch cannot execute", Rule{
			Trigger: TriggerIssueCreated, Actions: []RuleAction{RuleAction("send_email")}}, "send_email"},
		{"a condition field the engine cannot evaluate", Rule{
			Trigger:    TriggerIssueCreated,
			Conditions: []RuleCondition{{Field: "assignee_id", Operator: "eq", Value: "u-1"}},
			Actions:    []RuleAction{ActionCloseIssue}}, "assignee_id"},
		{"a condition operator the engine cannot apply", Rule{
			Trigger:    TriggerIssueCreated,
			Conditions: []RuleCondition{{Field: "priority", Operator: "gt", Value: "2"}},
			Actions:    []RuleAction{ActionCloseIssue}}, "gt"},
	}

	for _, c := range cases {
		r := c.rule
		r.WorkspaceID, r.TeamID, r.Name = wsID, teamID, c.name
		_, err := e.AddRule(context.Background(), r)
		if err == nil {
			t.Errorf("AddRule accepted %s.\n"+
				"The rule is persisted, listed back by GET /automation/rules, and can never run. "+
				"A 201 is the only signal the caller ever gets.", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantWord) {
			t.Errorf("AddRule refused %s but the message does not name %q: %v",
				c.name, c.wantWord, err)
		}
	}
}

// THE OTHER DIRECTION. "Refuse everything" satisfies the test above and deletes the
// feature, so every value the engine CAN act on is pinned as accepted.
func TestAddRule_AcceptsEveryVocabularyTheEngineCanActOn_RealPG(t *testing.T) {
	e, wsID, teamID := vocabFixture(t)

	for _, trig := range []RuleTrigger{
		TriggerIssueCreated, TriggerIssueUpdated, TriggerStatusChanged,
		TriggerAssigneeChanged, TriggerPRMerged, TriggerPROpened,
	} {
		if _, err := e.AddRule(context.Background(), Rule{
			WorkspaceID: wsID, TeamID: teamID, Name: "t-" + string(trig),
			Trigger: trig, Actions: []RuleAction{ActionCloseIssue},
		}); err != nil {
			t.Errorf("AddRule refused the firable trigger %q: %v", trig, err)
		}
	}

	for _, act := range []RuleAction{
		ActionSetStatus, ActionSetPriority, ActionSetAssignee, ActionAddLabel,
		ActionRemoveLabel, ActionCreateIssue, ActionNotifySlack, ActionCloseIssue,
		ActionMoveToCycle,
	} {
		if _, err := e.AddRule(context.Background(), Rule{
			WorkspaceID: wsID, TeamID: teamID, Name: "a-" + string(act),
			Trigger: TriggerIssueCreated, Actions: []RuleAction{act},
		}); err != nil {
			t.Errorf("AddRule refused the executable action %q: %v", act, err)
		}
	}

	for _, f := range []string{"status", "priority", "assignee", "label"} {
		for _, op := range []string{"eq", "neq", "contains", "not_contains"} {
			if _, err := e.AddRule(context.Background(), Rule{
				WorkspaceID: wsID, TeamID: teamID, Name: "c-" + f + "-" + op,
				Trigger:    TriggerIssueCreated,
				Conditions: []RuleCondition{{Field: f, Operator: op, Value: "x"}},
				Actions:    []RuleAction{ActionCloseIssue},
			}); err != nil {
				t.Errorf("AddRule refused the evaluable condition %s/%s: %v", f, op, err)
			}
		}
	}
}

// THE ALLOWLISTS MUST AGREE WITH THE DISPATCH, AND THAT IS CHECKED BY RUNNING THEM.
// An allowlist is a second statement of what the switch already says, and two statements
// of one fact are how the halves start disagreeing quietly — an allowlist entry whose
// dispatch arm was deleted would accept a rule that can never run, which is the very
// defect this file exists to close, reintroduced one layer up.
func TestVocabularyAllowlists_AgreeWithTheDispatch(t *testing.T) {
	e := newEngine(nil, &fakeIssueUpdater{}, &fakeSlack{})
	iss := model.Issue{ID: "i-1", WorkspaceID: "ws-1", TeamID: "team-1", Title: "t"}

	for act := range executableActions {
		err := e.executeAction(context.Background(), act, map[string]string{
			"status": "done", "priority": "1", "assignee_id": "u-1",
			"label": "bug", "cycle_id": "c-1", "title": "child",
			"webhook_url": "https://hooks.example.com/x",
		}, iss)
		if err != nil && strings.Contains(err.Error(), "unknown action") {
			t.Errorf("executableActions lists %q but the dispatch has no arm for it: %v", act, err)
		}
	}

	// Every allowlisted field must have a LIVE arm: some issue makes it evaluate true.
	live := map[string]model.Issue{
		"status":   {Status: model.StatusDone},
		"priority": {Priority: 3},
		"assignee": {AssigneeID: strPtr("u-1")},
		"label":    {Labels: []string{"bug"}},
	}
	want := map[string]string{"status": "done", "priority": "3", "assignee": "u-1", "label": "bug"}
	for f := range evaluableConditionFields {
		got := e.evaluateCondition(RuleCondition{Field: f, Operator: "eq", Value: want[f]}, live[f])
		if !got {
			t.Errorf("evaluableConditionFields lists %q but evaluateCondition never returns true for it — dead arm", f)
		}
	}

	for op := range applicableConditionOperators {
		hitStr := compare("bug", op, "bug")
		missStr := compare("bug", op, "zzz")
		if hitStr == missStr {
			t.Errorf("applicableConditionOperators lists %q but compare() cannot distinguish a hit from a miss — dead arm", op)
		}
		hitLbl := compareLabels([]string{"bug"}, op, "bug")
		missLbl := compareLabels([]string{"bug"}, op, "zzz")
		if hitLbl == missLbl {
			t.Errorf("applicableConditionOperators lists %q but compareLabels() cannot distinguish a hit from a miss — dead arm", op)
		}
	}
}

// TestTriggerVocabulary_TheDeclaredSetIsNotTheFirableSet pins the gap this item found:
// `scheduled` is a declared, exported trigger constant that NO production code path ever
// passes to Fire. It is not a bug in the allowlist — it is a capability the type declares
// and the server does not have, and this test is where that fact is written down so it
// cannot widen quietly.
//
// If this test fails, exactly one of two things happened and the fix differs:
//   - a NEW trigger constant was added with no producer -> either wire a producer, or
//     add it here deliberately and say why;
//   - `scheduled` GAINED a producer (a scheduler was built) -> add it to
//     firableTriggers and delete it from this list.
func TestTriggerVocabulary_TheDeclaredSetIsNotTheFirableSet(t *testing.T) {
	declared := []RuleTrigger{
		TriggerIssueCreated, TriggerIssueUpdated, TriggerStatusChanged,
		TriggerAssigneeChanged, TriggerPRMerged, TriggerPROpened, TriggerScheduled,
	}
	if len(declared) != 7 {
		t.Fatalf("the declared trigger census is %d, expected 7 — update this test WITH the constants", len(declared))
	}

	var notFirable []RuleTrigger
	for _, d := range declared {
		if !firableTriggers[d] {
			notFirable = append(notFirable, d)
		}
	}
	if len(notFirable) != 1 || notFirable[0] != TriggerScheduled {
		t.Fatalf("triggers declared but not firable = %v, want exactly [%s].\n"+
			"A declared trigger with no producer is a capability the API advertises and "+
			"the server does not have.", notFirable, TriggerScheduled)
	}
	if len(firableTriggers) != 6 {
		t.Errorf("firableTriggers has %d entries, want 6", len(firableTriggers))
	}
}

func strPtr(s string) *string { return &s }
