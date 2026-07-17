// Package automation is the rule engine that fires actions in
// response to issue lifecycle events. Rules belong to a workspace,
// match a trigger ("issue.created", "status.changed", "pr.merged"…),
// evaluate AND-joined conditions against the issue, and execute one
// or more actions when every condition passes.
//
// Errors at every step are LOGGED but never propagated. The
// triggering request (issue create, PR webhook, etc.) must never
// fail because automation misbehaved.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/tenancy"
)

type RuleTrigger string

const (
	TriggerIssueCreated    RuleTrigger = "issue.created"
	TriggerIssueUpdated    RuleTrigger = "issue.updated"
	TriggerStatusChanged   RuleTrigger = "status.changed"
	TriggerAssigneeChanged RuleTrigger = "assignee.changed"
	TriggerPRMerged        RuleTrigger = "pr.merged"
	TriggerPROpened        RuleTrigger = "pr.opened"
	TriggerScheduled       RuleTrigger = "scheduled"
)

type RuleCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

type RuleAction string

const (
	ActionSetStatus   RuleAction = "set_status"
	ActionSetPriority RuleAction = "set_priority"
	ActionSetAssignee RuleAction = "set_assignee"
	ActionAddLabel    RuleAction = "add_label"
	ActionRemoveLabel RuleAction = "remove_label"
	ActionCreateIssue RuleAction = "create_issue"
	ActionNotifySlack RuleAction = "notify_slack"
	ActionCloseIssue  RuleAction = "close_issue"
	ActionMoveToCycle RuleAction = "move_to_cycle"
)

type Rule struct {
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspace_id"`
	TeamID      string            `json:"team_id"`
	Name        string            `json:"name"`
	Enabled     bool              `json:"enabled"`
	Trigger     RuleTrigger       `json:"trigger"`
	Conditions  []RuleCondition   `json:"conditions"`
	Actions     []RuleAction      `json:"actions"`
	ActionData  map[string]string `json:"action_data"`
	CreatedAt   time.Time         `json:"created_at"`
}

// MaxRulesPerWorkspace caps how many rules a workspace can configure.
// 50 is generous — Linear's automation tier is comparable.
const MaxRulesPerWorkspace = 50

// MaxActionsPerRule keeps a single rule from spawning a runaway
// action chain. 10 is enough for any reasonable automation flow.
const MaxActionsPerRule = 10

type pgxDB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// issueUpdater is the slice of internal/issue.Store the engine uses.
// Local interface keeps tests cheap (no pgxmock for the full store).
type issueUpdater interface {
	Update(ctx context.Context, id, workspaceID string, updates map[string]any) (*model.Issue, error)
	Create(ctx context.Context, issue model.Issue) (*model.Issue, error)
	CreateComment(ctx context.Context, c model.Comment, workspaceID string) (*model.Comment, error)
}

type slackSender interface {
	Send(webhookURL, message string, blocks []map[string]any) error
}

type Engine struct {
	pool   pgxDB
	issues issueUpdater
	slack  slackSender

	mu    sync.RWMutex
	rules map[string][]Rule // workspaceID → loaded rules
}

func New(pool *pgxpool.Pool, issues issueUpdater, slack slackSender) *Engine {
	var db pgxDB
	if pool != nil {
		db = pool
	}
	return newEngine(db, issues, slack)
}

func newEngine(db pgxDB, issues issueUpdater, slack slackSender) *Engine {
	return &Engine{
		pool:   db,
		issues: issues,
		slack:  slack,
		rules:  make(map[string][]Rule),
	}
}

// LoadRules pulls the workspace's active rules into the in-memory
// cache. Call once at boot per known workspace, and again whenever
// AddRule / DeleteRule mutates the set.
func (e *Engine) LoadRules(ctx context.Context, workspaceID string) error {
	if e.pool == nil {
		return nil
	}
	rows, err := e.pool.Query(ctx,
		`SELECT id, workspace_id, team_id, name, enabled, trigger,
            conditions, actions, action_data, created_at
        FROM automation_rules
        WHERE workspace_id = $1 AND enabled = true`,
		workspaceID,
	)
	if err != nil {
		return fmt.Errorf("automation: load rules: %w", err)
	}
	defer rows.Close()

	var loaded []Rule
	for rows.Next() {
		var (
			r             Rule
			conditionsRaw []byte
			actionsStr    []string
			actionDataRaw []byte
		)
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.TeamID, &r.Name, &r.Enabled,
			&r.Trigger, &conditionsRaw, &actionsStr, &actionDataRaw, &r.CreatedAt); err != nil {
			return err
		}
		_ = json.Unmarshal(conditionsRaw, &r.Conditions)
		_ = json.Unmarshal(actionDataRaw, &r.ActionData)
		for _, a := range actionsStr {
			r.Actions = append(r.Actions, RuleAction(a))
		}
		loaded = append(loaded, r)
	}
	e.mu.Lock()
	e.rules[workspaceID] = loaded
	e.mu.Unlock()
	return rows.Err()
}

// AddRule validates and persists a new rule, then updates the cache.
// The per-workspace and per-rule limits enforce the documented caps.
func (e *Engine) AddRule(ctx context.Context, rule Rule) (*Rule, error) {
	if rule.WorkspaceID == "" || rule.TeamID == "" || rule.Name == "" || rule.Trigger == "" {
		return nil, errors.New("automation: WorkspaceID, TeamID, Name, Trigger required")
	}
	if len(rule.Actions) == 0 {
		return nil, errors.New("automation: at least one action required")
	}
	if len(rule.Actions) > MaxActionsPerRule {
		return nil, fmt.Errorf("automation: %d actions exceeds maximum %d", len(rule.Actions), MaxActionsPerRule)
	}
	if rule.ActionData == nil {
		rule.ActionData = map[string]string{}
	}
	rule.Enabled = true

	e.mu.RLock()
	existing := len(e.rules[rule.WorkspaceID])
	e.mu.RUnlock()
	if existing >= MaxRulesPerWorkspace {
		return nil, fmt.Errorf("automation: workspace already has %d rules (max %d)", existing, MaxRulesPerWorkspace)
	}

	if e.pool != nil {
		// Cross-object tenancy: a rule's team must live in the rule's own
		// workspace. TeamID is required above, but the empty guard keeps
		// this consistent with the other cross-object sites.
		if rule.TeamID != "" {
			if err := tenancy.AssertRefInWorkspace(ctx, e.pool, "teams", rule.TeamID, rule.WorkspaceID); err != nil {
				return nil, err
			}
		}

		conditionsJSON, _ := json.Marshal(rule.Conditions)
		actionDataJSON, _ := json.Marshal(rule.ActionData)
		actionsStr := make([]string, len(rule.Actions))
		for i, a := range rule.Actions {
			actionsStr[i] = string(a)
		}
		var (
			id        string
			createdAt time.Time
		)
		err := e.pool.QueryRow(ctx,
			`INSERT INTO automation_rules
                (workspace_id, team_id, name, enabled, trigger, conditions, actions, action_data)
            VALUES ($1, $2, $3, true, $4, $5, $6, $7)
            RETURNING id, created_at`,
			rule.WorkspaceID, rule.TeamID, rule.Name, string(rule.Trigger),
			conditionsJSON, actionsStr, actionDataJSON,
		).Scan(&id, &createdAt)
		if err != nil {
			return nil, fmt.Errorf("automation: insert rule: %w", err)
		}
		rule.ID = id
		rule.CreatedAt = createdAt
	}

	e.mu.Lock()
	e.rules[rule.WorkspaceID] = append(e.rules[rule.WorkspaceID], rule)
	e.mu.Unlock()
	return &rule, nil
}

// Fire is the engine's hot path: find every cached rule for the
// (workspace, trigger) pair, evaluate AND conditions, execute
// actions, log the result. Errors at every stage are recorded in
// automation_logs but never propagated — automation must never break
// the triggering request.
func (e *Engine) Fire(ctx context.Context, trigger RuleTrigger, workspaceID string, issue model.Issue, changes map[string]any) error {
	e.mu.RLock()
	rules := append([]Rule(nil), e.rules[workspaceID]...)
	e.mu.RUnlock()

	for _, r := range rules {
		if !r.Enabled || r.Trigger != trigger {
			continue
		}
		if !e.conditionsPass(r.Conditions, issue) {
			continue
		}

		var (
			actionsTaken []string
			runErr       error
		)
		for _, action := range r.Actions {
			if err := e.executeAction(ctx, action, r.ActionData, issue); err != nil {
				runErr = err
				slog.Warn("automation: action failed",
					slog.String("rule_id", r.ID),
					slog.String("action", string(action)),
					slog.String("err", err.Error()),
				)
				continue
			}
			actionsTaken = append(actionsTaken, string(action))
		}
		e.logRun(ctx, r.ID, issue.ID, trigger, actionsTaken, runErr)
	}
	return nil
}

func (e *Engine) conditionsPass(conditions []RuleCondition, issue model.Issue) bool {
	// Empty condition list = "always pass". Useful for rules like
	// "every newly-created issue → notify Slack".
	for _, c := range conditions {
		if !e.evaluateCondition(c, issue) {
			return false
		}
	}
	return true
}

func (e *Engine) evaluateCondition(c RuleCondition, issue model.Issue) bool {
	switch c.Field {
	case "status":
		return compare(string(issue.Status), c.Operator, c.Value)
	case "priority":
		return compare(strconv.Itoa(int(issue.Priority)), c.Operator, c.Value)
	case "assignee":
		assignee := ""
		if issue.AssigneeID != nil {
			assignee = *issue.AssigneeID
		}
		return compare(assignee, c.Operator, c.Value)
	case "label":
		return compareLabels(issue.Labels, c.Operator, c.Value)
	}
	return false
}

func compare(got, op, want string) bool {
	switch op {
	case "eq":
		return got == want
	case "neq":
		return got != want
	case "contains":
		return strings.Contains(got, want)
	case "not_contains":
		return !strings.Contains(got, want)
	}
	return false
}

func compareLabels(labels []string, op, value string) bool {
	has := false
	for _, l := range labels {
		if l == value {
			has = true
			break
		}
	}
	switch op {
	case "eq", "contains":
		return has
	case "neq", "not_contains":
		return !has
	}
	return false
}

// executeAction is the dispatch table for the action enum. New
// actions add a case here plus their handling in the rule editor UI.
func (e *Engine) executeAction(ctx context.Context, action RuleAction, data map[string]string, issue model.Issue) error {
	switch action {
	case ActionSetStatus:
		_, err := e.issues.Update(ctx, issue.ID, issue.WorkspaceID, map[string]any{"status": data["status"]})
		return err
	case ActionSetPriority:
		n, err := strconv.Atoi(data["priority"])
		if err != nil {
			return fmt.Errorf("set_priority: priority must be int: %w", err)
		}
		_, err = e.issues.Update(ctx, issue.ID, issue.WorkspaceID, map[string]any{"priority": n})
		return err
	case ActionSetAssignee:
		_, err := e.issues.Update(ctx, issue.ID, issue.WorkspaceID, map[string]any{"assignee_id": data["assignee_id"]})
		return err
	case ActionAddLabel:
		label := data["label"]
		if label == "" {
			return errors.New("add_label: missing label in action_data")
		}
		updated := append(append([]string(nil), issue.Labels...), label)
		_, err := e.issues.Update(ctx, issue.ID, issue.WorkspaceID, map[string]any{"labels": updated})
		return err
	case ActionRemoveLabel:
		label := data["label"]
		filtered := make([]string, 0, len(issue.Labels))
		for _, l := range issue.Labels {
			if l != label {
				filtered = append(filtered, l)
			}
		}
		_, err := e.issues.Update(ctx, issue.ID, issue.WorkspaceID, map[string]any{"labels": filtered})
		return err
	case ActionCloseIssue:
		_, err := e.issues.Update(ctx, issue.ID, issue.WorkspaceID, map[string]any{"status": string(model.StatusDone)})
		return err
	case ActionMoveToCycle:
		_, err := e.issues.Update(ctx, issue.ID, issue.WorkspaceID, map[string]any{"cycle_id": data["cycle_id"]})
		return err
	case ActionCreateIssue:
		// Create a real child issue linked to the triggering issue. Attribution: the
		// rule carries no creator, so the child inherits the parent issue's creator_id
		// — the closest attributable human (creator_id is NOT NULL and not
		// FK-constrained). Falls back to the "automation" sentinel only if the parent
		// somehow has no creator.
		title := data["title"]
		if title == "" {
			return errors.New("create_issue: action_data.title required")
		}
		creator := issue.CreatorID
		if creator == "" {
			creator = "automation"
		}
		parentID := issue.ID
		_, err := e.issues.Create(ctx, model.Issue{
			WorkspaceID: issue.WorkspaceID,
			TeamID:      issue.TeamID,
			Title:       title,
			CreatorID:   creator,
			ParentID:    &parentID,
			Status:      model.StatusBacklog,
		})
		return err
	case ActionNotifySlack:
		if e.slack == nil {
			return errors.New("notify_slack: no slack sender configured")
		}
		url := data["webhook_url"]
		if url == "" {
			return errors.New("notify_slack: webhook_url required")
		}
		msg := fmt.Sprintf("*%s* — %s", issue.Identifier, issue.Title)
		return e.slack.Send(url, msg, nil)
	}
	return fmt.Errorf("unknown action: %s", action)
}

// logRun persists one row per Fire invocation. Used by the
// /v1/.../automation/logs endpoint so operators can audit what fired
// and when.
func (e *Engine) logRun(ctx context.Context, ruleID, issueID string, trigger RuleTrigger, actions []string, runErr error) {
	if e.pool == nil {
		return
	}
	success := runErr == nil
	errStr := ""
	if runErr != nil {
		errStr = runErr.Error()
	}
	// issue_id is nullable in the schema; pass nil when empty.
	var issuePtr any
	if issueID != "" {
		issuePtr = issueID
	}
	if _, err := e.pool.Exec(ctx,
		`INSERT INTO automation_logs (rule_id, issue_id, trigger, actions_taken, success, error)
        VALUES ($1, $2, $3, $4, $5, $6)`,
		ruleID, issuePtr, string(trigger), actions, success, errStr,
	); err != nil {
		slog.Warn("automation: log persist failed",
			slog.String("rule_id", ruleID),
			slog.String("err", err.Error()),
		)
	}
}

// DeleteRule removes a rule from the cache + DB. Calling on an
// unknown rule is a no-op.
// ErrNotFound is the SEC-5 sentinel: a by-id rule op resolved to no row in the caller's authorized
// workspace. The handler maps it to 404 (a foreign id and a nonexistent id are indistinguishable).
var ErrNotFound = errors.New("automation: rule not found in workspace")

func (e *Engine) DeleteRule(ctx context.Context, ruleID, workspaceID string) error {
	if e.pool != nil {
		// SEC-5: scope the delete to the caller's authorized workspace — a foreign rule is ErrNotFound.
		ct, err := e.pool.Exec(ctx, `DELETE FROM automation_rules WHERE id = $1 AND workspace_id = $2`, ruleID, workspaceID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	e.mu.Lock()
	for ws, rules := range e.rules {
		filtered := rules[:0]
		for _, r := range rules {
			if r.ID != ruleID {
				filtered = append(filtered, r)
			}
		}
		e.rules[ws] = filtered
	}
	e.mu.Unlock()
	return nil
}

// ListRules returns the in-memory cached rules for a workspace.
func (e *Engine) ListRules(workspaceID string) []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Rule, len(e.rules[workspaceID]))
	copy(out, e.rules[workspaceID])
	return out
}
