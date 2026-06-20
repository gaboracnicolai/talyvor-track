package automation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/talyvor/track/internal/model"
)

// fakeIssueUpdater captures the updates fired by Fire/executeAction
// so tests can assert the right rule actions ran.
type fakeIssueUpdater struct {
	mu       sync.Mutex
	updates  []map[string]any
	comments []model.Comment
}

func (f *fakeIssueUpdater) Update(_ context.Context, _ string, updates map[string]any) (*model.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, updates)
	return &model.Issue{ID: "i-1"}, nil
}
func (f *fakeIssueUpdater) CreateComment(_ context.Context, c model.Comment) (*model.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, c)
	return &c, nil
}

type fakeSlack struct {
	mu    sync.Mutex
	calls []string
}

func (s *fakeSlack) Send(url, msg string, _ []map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, url+"|"+msg)
	return nil
}

// withCachedRule preloads a single rule into the engine's cache so
// tests don't need a DB. The rule has Enabled=true so Fire picks it up.
func withCachedRule(e *Engine, rule Rule) {
	rule.Enabled = true
	e.mu.Lock()
	e.rules[rule.WorkspaceID] = append(e.rules[rule.WorkspaceID], rule)
	e.mu.Unlock()
}

func newTestEngine() (*Engine, *fakeIssueUpdater, *fakeSlack) {
	updater := &fakeIssueUpdater{}
	slack := &fakeSlack{}
	e := newEngine(nil, updater, slack)
	return e, updater, slack
}

func TestFire_ExecutesMatchingRule(t *testing.T) {
	e, updater, _ := newTestEngine()
	withCachedRule(e, Rule{
		ID: "r-1", WorkspaceID: "ws-1", TeamID: "team-1", Name: "Auto-close",
		Trigger: TriggerStatusChanged,
		Conditions: []RuleCondition{
			{Field: "label", Operator: "contains", Value: "bug"},
		},
		Actions:    []RuleAction{ActionAddLabel},
		ActionData: map[string]string{"label": "auto-triaged"},
	})

	err := e.Fire(context.Background(), TriggerStatusChanged, "ws-1", model.Issue{
		ID: "i-1", Labels: []string{"bug"},
	}, nil)
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if len(updater.updates) != 1 {
		t.Fatalf("got %d updates, want 1", len(updater.updates))
	}
	got := updater.updates[0]["labels"].([]string)
	if len(got) != 2 || got[1] != "auto-triaged" {
		t.Errorf("labels = %v, want [bug auto-triaged]", got)
	}
}

func TestFire_SkipsDisabledRules(t *testing.T) {
	e, updater, _ := newTestEngine()
	withCachedRule(e, Rule{
		ID: "r-disabled", WorkspaceID: "ws-1", TeamID: "team-1",
		Trigger: TriggerStatusChanged,
		Actions: []RuleAction{ActionCloseIssue},
	})
	// Force-disable the rule we just added (withCachedRule sets enabled=true).
	e.mu.Lock()
	e.rules["ws-1"][0].Enabled = false
	e.mu.Unlock()

	_ = e.Fire(context.Background(), TriggerStatusChanged, "ws-1", model.Issue{ID: "i-1"}, nil)
	if len(updater.updates) != 0 {
		t.Errorf("disabled rule fired anyway: %d updates", len(updater.updates))
	}
}

func TestFire_SkipsRulesWithFailedConditions(t *testing.T) {
	e, updater, _ := newTestEngine()
	withCachedRule(e, Rule{
		ID: "r-1", WorkspaceID: "ws-1", TeamID: "team-1",
		Trigger: TriggerIssueUpdated,
		Conditions: []RuleCondition{
			{Field: "status", Operator: "eq", Value: "done"},
		},
		Actions:    []RuleAction{ActionAddLabel},
		ActionData: map[string]string{"label": "shouldn't fire"},
	})

	// Issue status is "in_progress", not "done" — condition fails.
	_ = e.Fire(context.Background(), TriggerIssueUpdated, "ws-1", model.Issue{
		ID: "i-1", Status: model.StatusInProgress,
	}, nil)
	if len(updater.updates) != 0 {
		t.Errorf("rule fired despite failed condition: %d updates", len(updater.updates))
	}
}

func TestEvaluateCondition_StatusEqMatches(t *testing.T) {
	e, _, _ := newTestEngine()
	got := e.evaluateCondition(
		RuleCondition{Field: "status", Operator: "eq", Value: "done"},
		model.Issue{Status: model.StatusDone},
	)
	if !got {
		t.Error("status=done with op=eq should match")
	}
}

func TestEvaluateCondition_LabelContainsMatches(t *testing.T) {
	e, _, _ := newTestEngine()
	got := e.evaluateCondition(
		RuleCondition{Field: "label", Operator: "contains", Value: "bug"},
		model.Issue{Labels: []string{"frontend", "bug", "urgent"}},
	)
	if !got {
		t.Error("issue with 'bug' label should match contains:bug")
	}
}

func TestExecuteAction_SetStatusUpdatesIssue(t *testing.T) {
	e, updater, _ := newTestEngine()
	err := e.executeAction(context.Background(), ActionSetStatus,
		map[string]string{"status": "in_review"},
		model.Issue{ID: "i-1"},
	)
	if err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if len(updater.updates) != 1 || updater.updates[0]["status"] != "in_review" {
		t.Errorf("set_status did not update; got %+v", updater.updates)
	}
}

func TestExecuteAction_AddLabelAppendsLabel(t *testing.T) {
	e, updater, _ := newTestEngine()
	err := e.executeAction(context.Background(), ActionAddLabel,
		map[string]string{"label": "needs-review"},
		model.Issue{ID: "i-1", Labels: []string{"existing"}},
	)
	if err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	got := updater.updates[0]["labels"].([]string)
	if len(got) != 2 || got[0] != "existing" || got[1] != "needs-review" {
		t.Errorf("labels = %v, want [existing needs-review]", got)
	}
}

func TestExecuteAction_NotifySlackCallsSender(t *testing.T) {
	e, _, slack := newTestEngine()
	err := e.executeAction(context.Background(), ActionNotifySlack,
		map[string]string{"webhook_url": "https://hooks.slack.com/x"},
		model.Issue{Identifier: "ENG-42", Title: "Bug"},
	)
	if err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if len(slack.calls) != 1 {
		t.Fatalf("got %d Slack calls, want 1", len(slack.calls))
	}
	if slack.calls[0] != "https://hooks.slack.com/x|*ENG-42* — Bug" {
		t.Errorf("Slack message wrong: %q", slack.calls[0])
	}
}

func TestAddRule_GuardsTeamRef(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	e := newEngine(pool, &fakeIssueUpdater{}, &fakeSlack{})

	// Cross-object guard verifies the team is in the rule's workspace
	// before the INSERT.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-1", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	pool.ExpectQuery(`INSERT INTO automation_rules`).
		WithArgs("ws-1", "team-1", "Auto", string(TriggerIssueCreated),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).
			AddRow("r-1", time.Now().UTC()))

	out, err := e.AddRule(context.Background(), Rule{
		WorkspaceID: "ws-1", TeamID: "team-1", Name: "Auto",
		Trigger: TriggerIssueCreated,
		Actions: []RuleAction{ActionAddLabel},
	})
	if err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if out.ID != "r-1" {
		t.Errorf("got %+v", out)
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestAddRule_CrossWorkspaceTeamRejected(t *testing.T) {
	pool, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	t.Cleanup(pool.Close)
	e := newEngine(pool, &fakeIssueUpdater{}, &fakeSlack{})

	// Team in another workspace → EXISTS false → reject before INSERT.
	pool.ExpectQuery(`SELECT EXISTS`).
		WithArgs("team-other", "ws-1").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	_, err = e.AddRule(context.Background(), Rule{
		WorkspaceID: "ws-1", TeamID: "team-other", Name: "Auto",
		Trigger: TriggerIssueCreated,
		Actions: []RuleAction{ActionAddLabel},
	})
	if err == nil {
		t.Fatal("expected cross-workspace team ref to be rejected")
	}
	if len(e.ListRules("ws-1")) != 0 {
		t.Error("rejected rule must not enter the cache")
	}
	if err := pool.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestAddRule_RejectsTooManyActions(t *testing.T) {
	e := newEngine(nil, &fakeIssueUpdater{}, &fakeSlack{})
	actions := make([]RuleAction, MaxActionsPerRule+1)
	for i := range actions {
		actions[i] = ActionAddLabel
	}
	_, err := e.AddRule(context.Background(), Rule{
		WorkspaceID: "ws-1", TeamID: "team-1", Name: "too-many",
		Trigger: TriggerIssueCreated,
		Actions: actions,
	})
	if err == nil {
		t.Error("AddRule should reject rule with > MaxActionsPerRule actions")
	}
}
