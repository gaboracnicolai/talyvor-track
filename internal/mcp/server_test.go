package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/ai"
	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
)

// ─── fakes ──────────────────────────────────────────────────
//
// Each fake implements one of the package-internal store/engine
// interfaces. They are intentionally closure-based — tests set
// just the methods they need and leave the rest unimplemented;
// any unset method panics so missed expectations surface loudly.

type fakeIssueStore struct {
	createFn        func(context.Context, model.Issue) (*model.Issue, error)
	getByIDFn       func(context.Context, string) (*model.Issue, error)
	getByIdentFn    func(context.Context, string) (*model.Issue, error)
	listFn          func(context.Context, issue.IssueFilter) ([]model.Issue, error)
	updateFn        func(context.Context, string, map[string]any) (*model.Issue, error)
	searchFn        func(context.Context, string, string, int) ([]model.Issue, error)
	createCommentFn func(context.Context, model.Comment) (*model.Comment, error)
}

func (f *fakeIssueStore) Create(ctx context.Context, i model.Issue) (*model.Issue, error) {
	return f.createFn(ctx, i)
}
func (f *fakeIssueStore) GetByID(ctx context.Context, id string) (*model.Issue, error) {
	return f.getByIDFn(ctx, id)
}
func (f *fakeIssueStore) GetByIdentifier(ctx context.Context, ident string) (*model.Issue, error) {
	return f.getByIdentFn(ctx, ident)
}
func (f *fakeIssueStore) List(ctx context.Context, filter issue.IssueFilter) ([]model.Issue, error) {
	return f.listFn(ctx, filter)
}
func (f *fakeIssueStore) Update(ctx context.Context, id string, updates map[string]any) (*model.Issue, error) {
	return f.updateFn(ctx, id, updates)
}
func (f *fakeIssueStore) Search(ctx context.Context, ws, q string, limit int) ([]model.Issue, error) {
	return f.searchFn(ctx, ws, q, limit)
}
func (f *fakeIssueStore) CreateComment(ctx context.Context, c model.Comment) (*model.Comment, error) {
	return f.createCommentFn(ctx, c)
}

type fakeProjectStore struct {
	createFn func(context.Context, model.Project) (*model.Project, error)
}

func (f *fakeProjectStore) Create(ctx context.Context, p model.Project) (*model.Project, error) {
	return f.createFn(ctx, p)
}

type fakeCycleStore struct {
	getActiveFn   func(context.Context, string) (*model.Cycle, error)
	getProgressFn func(context.Context, string) (*cycle.CycleProgress, error)
}

func (f *fakeCycleStore) GetActive(ctx context.Context, teamID string) (*model.Cycle, error) {
	return f.getActiveFn(ctx, teamID)
}
func (f *fakeCycleStore) GetProgress(ctx context.Context, cycleID string) (*cycle.CycleProgress, error) {
	return f.getProgressFn(ctx, cycleID)
}

type fakeAIEngine struct {
	available bool
	triageFn  func(context.Context, model.Issue) (*ai.TriageResult, error)
}

func (f *fakeAIEngine) IsAvailable() bool { return f.available }
func (f *fakeAIEngine) TriageIssue(ctx context.Context, i model.Issue) (*ai.TriageResult, error) {
	return f.triageFn(ctx, i)
}

type fakeAnalytics struct {
	aiCostsFn func(context.Context, string, int) (*analytics.AICostTrends, error)
}

func (f *fakeAnalytics) GetAICostTrends(ctx context.Context, ws string, days int) (*analytics.AICostTrends, error) {
	return f.aiCostsFn(ctx, ws, days)
}

type fakeMembers struct {
	listFn   func(context.Context, string, string) ([]model.Member, error)
	teamWsFn func(context.Context, string) (string, error)
}

func (f *fakeMembers) ListMembers(ctx context.Context, ws, team string) ([]model.Member, error) {
	return f.listFn(ctx, ws, team)
}

func (f *fakeMembers) WorkspaceOfTeam(ctx context.Context, teamID string) (string, error) {
	if f.teamWsFn != nil {
		return f.teamWsFn(ctx, teamID)
	}
	return "ws-1", nil // default: unit tests act in ws-1
}

// ─── helpers ────────────────────────────────────────────────

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return newServer(
		&fakeIssueStore{},
		&fakeProjectStore{},
		&fakeCycleStore{},
		&fakeAIEngine{available: false},
		&fakeAnalytics{},
		&fakeMembers{},
		"test-version",
	)
}

// rpcCall fires a JSON-RPC request through HandleRPC and returns the
// parsed response envelope.
func rpcCall(t *testing.T, s *Server, method string, params any) rpcResponse {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	enc, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(enc))
	// The production chain (gatewayauth + authz) runs before HandleRPC and seeds the
	// caller's memberships; these unit tests exercise tool logic, so inject a membership for
	// the workspace they act in (ws-1). The dedicated authz proof (authz_integration_test.go)
	// drives the REAL middleware to test deny/allow.
	req = req.WithContext(authz.WithMemberships(req.Context(),
		[]authz.Membership{{WorkspaceID: "ws-1", MemberID: "m-test"}}))
	w := httptest.NewRecorder()
	s.HandleRPC(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp rpcResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode JSON-RPC: %v; body=%s", err, w.Body.String())
	}
	return resp
}

// toolResult parses a tools/call success envelope into the inner JSON
// the MCP spec wraps in `content[0].text`.
func toolResult(t *testing.T, resp rpcResponse) map[string]any {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("expected no error, got %+v", resp.Error)
	}
	res, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result not an object: %T", resp.Result)
	}
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("content missing or empty: %v", res)
	}
	first := content[0].(map[string]any)
	if first["type"] != "text" {
		t.Fatalf("content[0].type = %v, want text", first["type"])
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(first["text"].(string)), &parsed); err != nil {
		t.Fatalf("decode content text: %v; text=%s", err, first["text"])
	}
	return parsed
}

// ─── tests ──────────────────────────────────────────────────

func TestMCP_InitializeReturnsProtocolVersion(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, "initialize", nil)
	if resp.Error != nil {
		t.Fatalf("initialize errored: %+v", resp.Error)
	}
	res := resp.Result.(map[string]any)
	if res["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", res["protocolVersion"])
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "talyvor-track" {
		t.Errorf("serverInfo.name = %v, want talyvor-track", info["name"])
	}
	if info["version"] != "test-version" {
		t.Errorf("serverInfo.version = %v, want test-version", info["version"])
	}
}

func TestMCP_ToolsList_ReturnsAll12Tools(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, "tools/list", nil)
	res := resp.Result.(map[string]any)
	tools := res["tools"].([]any)
	if len(tools) != 12 {
		t.Fatalf("got %d tools, want 12", len(tools))
	}
	want := map[string]bool{
		"create_issue":      true,
		"update_issue":      true,
		"get_issue":         true,
		"list_issues":       true,
		"search_issues":     true,
		"add_comment":       true,
		"get_sprint_status": true,
		"triage_issue":      true,
		"get_ai_costs":      true,
		"move_to_cycle":     true,
		"create_project":    true,
		"list_team_members": true,
	}
	for _, tl := range tools {
		entry := tl.(map[string]any)
		name, _ := entry["name"].(string)
		if !want[name] {
			t.Errorf("unexpected tool: %q", name)
		}
		// Descriptions must be present and non-trivial.
		if desc, _ := entry["description"].(string); len(desc) < 10 {
			t.Errorf("tool %q description too short: %q", name, desc)
		}
		delete(want, name)
	}
	if len(want) > 0 {
		t.Errorf("missing tools: %v", want)
	}
}

func TestMCP_CreateIssue_CreatesAndReturnsIssue(t *testing.T) {
	s := newTestServer(t)
	s.issueStore = &fakeIssueStore{
		createFn: func(_ context.Context, in model.Issue) (*model.Issue, error) {
			// Echo the input back as a freshly-stamped record.
			out := in
			out.ID = "i-1"
			out.Identifier = "ENG-1"
			out.Number = 1
			out.Status = model.StatusTodo
			return &out, nil
		},
	}

	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "create_issue",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"team_id":      "team-1",
			"title":        "Fix the bug",
			"description":  "Repro: cmd+k crashes",
			"priority":     2,
			"labels":       []string{"bug"},
		},
	})
	data := toolResult(t, resp)

	if data["id"] != "i-1" {
		t.Errorf("id = %v, want i-1", data["id"])
	}
	if data["identifier"] != "ENG-1" {
		t.Errorf("identifier = %v, want ENG-1", data["identifier"])
	}
	if data["title"] != "Fix the bug" {
		t.Errorf("title = %v, want Fix the bug", data["title"])
	}
	if int(data["priority"].(float64)) != 2 {
		t.Errorf("priority = %v, want 2", data["priority"])
	}
	if data["url"] == "" || data["url"] == nil {
		t.Errorf("url empty: %v", data["url"])
	}
}

func TestMCP_GetIssue_ReturnsCorrectIssue(t *testing.T) {
	s := newTestServer(t)
	s.issueStore = &fakeIssueStore{
		getByIDFn: func(_ context.Context, id string) (*model.Issue, error) {
			return &model.Issue{
				ID:          id,
				WorkspaceID: "ws-1",
				Identifier:  "ENG-42",
				Title:       "Slow query",
				Status:      model.StatusInProgress,
				Priority:    model.PriorityHigh,
				AICostUSD:   0.87,
				AITokens:    1500,
			}, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_issue",
		"arguments": map[string]any{"issue_id": "i-42"},
	})
	data := toolResult(t, resp)
	if data["identifier"] != "ENG-42" {
		t.Errorf("identifier = %v, want ENG-42", data["identifier"])
	}
	if data["ai_cost_usd"].(float64) != 0.87 {
		t.Errorf("ai_cost_usd = %v, want 0.87", data["ai_cost_usd"])
	}
	if int(data["ai_tokens"].(float64)) != 1500 {
		t.Errorf("ai_tokens = %v, want 1500", data["ai_tokens"])
	}
}

func TestMCP_GetIssue_LooksUpByIdentifier(t *testing.T) {
	s := newTestServer(t)
	called := ""
	s.issueStore = &fakeIssueStore{
		getByIdentFn: func(_ context.Context, ident string) (*model.Issue, error) {
			called = ident
			return &model.Issue{ID: "i-9", WorkspaceID: "ws-1", Identifier: ident, Title: "found by ident"}, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "get_issue",
		"arguments": map[string]any{"identifier": "ENG-9"},
	})
	data := toolResult(t, resp)
	if called != "ENG-9" {
		t.Errorf("getByIdentifier called with %q, want ENG-9", called)
	}
	if data["title"] != "found by ident" {
		t.Errorf("title = %v", data["title"])
	}
}

func TestMCP_ListIssues_ReturnsFilteredList(t *testing.T) {
	s := newTestServer(t)
	captured := issue.IssueFilter{}
	s.issueStore = &fakeIssueStore{
		listFn: func(_ context.Context, f issue.IssueFilter) ([]model.Issue, error) {
			captured = f
			return []model.Issue{
				{ID: "a", Identifier: "ENG-1", Status: model.StatusTodo, AICostUSD: 0.5},
				{ID: "b", Identifier: "ENG-2", Status: model.StatusInProgress, AICostUSD: 1.25},
			}, nil
		},
	}

	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "list_issues",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"team_id":      "team-1",
			"status":       "todo",
			"limit":        10,
		},
	})
	data := toolResult(t, resp)
	issues, _ := data["issues"].([]any)
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}
	if captured.WorkspaceID != "ws-1" || captured.TeamID != "team-1" || captured.Status != "todo" || captured.Limit != 10 {
		t.Errorf("filter not propagated: %+v", captured)
	}
	first := issues[0].(map[string]any)
	if first["identifier"] != "ENG-1" {
		t.Errorf("issues[0].identifier = %v", first["identifier"])
	}
	if first["ai_cost_usd"].(float64) != 0.5 {
		t.Errorf("issues[0].ai_cost_usd = %v, want 0.5", first["ai_cost_usd"])
	}
}

func TestMCP_ListIssues_CapsLimitAt100(t *testing.T) {
	s := newTestServer(t)
	captured := issue.IssueFilter{}
	s.issueStore = &fakeIssueStore{
		listFn: func(_ context.Context, f issue.IssueFilter) ([]model.Issue, error) {
			captured = f
			return []model.Issue{}, nil
		},
	}
	rpcCall(t, s, "tools/call", map[string]any{
		"name": "list_issues",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"limit":        99999,
		},
	})
	if captured.Limit != 100 {
		t.Errorf("limit not capped: %d, want 100", captured.Limit)
	}
}

func TestMCP_SearchIssues_ReturnsResults(t *testing.T) {
	s := newTestServer(t)
	s.issueStore = &fakeIssueStore{
		searchFn: func(_ context.Context, ws, q string, limit int) ([]model.Issue, error) {
			if ws != "ws-1" || q != "cache invalidation" || limit != 5 {
				return nil, errors.New("unexpected search args")
			}
			return []model.Issue{
				{ID: "a", Identifier: "ENG-3", Title: "cache invalidation bug"},
			}, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "search_issues",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"query":        "cache invalidation",
			"limit":        5,
		},
	})
	data := toolResult(t, resp)
	issues := data["issues"].([]any)
	if len(issues) != 1 {
		t.Fatalf("got %d, want 1", len(issues))
	}
}

func TestMCP_AddComment_CreatesComment(t *testing.T) {
	s := newTestServer(t)
	captured := model.Comment{}
	s.issueStore = &fakeIssueStore{
		getByIDFn: func(_ context.Context, id string) (*model.Issue, error) {
			return &model.Issue{ID: id, WorkspaceID: "ws-1"}, nil
		},
		createCommentFn: func(_ context.Context, c model.Comment) (*model.Comment, error) {
			captured = c
			return &model.Comment{
				ID:        "c-1",
				IssueID:   c.IssueID,
				AuthorID:  c.AuthorID,
				Body:      c.Body,
				CreatedAt: time.Now().UTC(),
			}, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "add_comment",
		"arguments": map[string]any{
			"issue_id": "i-1",
			"body":     "LGTM, shipping",
		},
	})
	data := toolResult(t, resp)
	if data["id"] != "c-1" {
		t.Errorf("id = %v", data["id"])
	}
	if data["body"] != "LGTM, shipping" {
		t.Errorf("body = %v", data["body"])
	}
	if captured.AuthorID != "m-test" {
		t.Errorf("author_id = %q, want the resolved member %q (not a caller-supplied/agent value)", captured.AuthorID, "m-test")
	}
}

func TestMCP_GetSprintStatus_ReturnsCycleProgress(t *testing.T) {
	s := newTestServer(t)
	now := time.Now().UTC()
	s.cycleStore = &fakeCycleStore{
		getActiveFn: func(_ context.Context, teamID string) (*model.Cycle, error) {
			return &model.Cycle{
				ID:        "cycle-1",
				TeamID:    teamID,
				Name:      "Sprint 17",
				StartDate: now.Add(-3 * 24 * time.Hour),
				EndDate:   now.Add(11 * 24 * time.Hour),
			}, nil
		},
		getProgressFn: func(_ context.Context, cycleID string) (*cycle.CycleProgress, error) {
			return &cycle.CycleProgress{
				CycleID:        cycleID,
				TotalIssues:    20,
				Completed:      8,
				InProgress:     5,
				CompletionPct:  0.4,
				TotalAICostUSD: 12.34,
			}, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "get_sprint_status",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"team_id":      "team-1",
		},
	})
	data := toolResult(t, resp)
	if data["cycle_name"] != "Sprint 17" {
		t.Errorf("cycle_name = %v", data["cycle_name"])
	}
	if int(data["total_issues"].(float64)) != 20 {
		t.Errorf("total_issues = %v", data["total_issues"])
	}
	if data["completion_pct"].(float64) != 0.4 {
		t.Errorf("completion_pct = %v", data["completion_pct"])
	}
	if data["ai_cost_usd"].(float64) != 12.34 {
		t.Errorf("ai_cost_usd = %v", data["ai_cost_usd"])
	}
}

func TestMCP_GetAICosts_ReturnsCostBreakdown(t *testing.T) {
	s := newTestServer(t)
	s.analytics = &fakeAnalytics{
		aiCostsFn: func(_ context.Context, _ string, days int) (*analytics.AICostTrends, error) {
			if days != 7 {
				return nil, errors.New("expected 7 days")
			}
			return &analytics.AICostTrends{
				TotalCostUSD:     45.67,
				ProjectedMonthly: 195.78,
				AvgCostPerIssue:  2.28,
				TopCostIssues: []analytics.IssueCost{
					{IssueID: "i-1", Identifier: "ENG-1", Title: "expensive A", CostUSD: 12.5, Tokens: 9000},
					{IssueID: "i-2", Identifier: "ENG-2", Title: "expensive B", CostUSD: 9.1, Tokens: 7000},
					{IssueID: "i-3", Identifier: "ENG-3", Title: "expensive C", CostUSD: 7.4, Tokens: 6000},
					{IssueID: "i-4", Identifier: "ENG-4", Title: "expensive D", CostUSD: 5.0, Tokens: 5000},
					{IssueID: "i-5", Identifier: "ENG-5", Title: "expensive E", CostUSD: 4.2, Tokens: 4000},
					{IssueID: "i-6", Identifier: "ENG-6", Title: "expensive F", CostUSD: 3.1, Tokens: 3000},
					{IssueID: "i-7", Identifier: "ENG-7", Title: "expensive G", CostUSD: 2.0, Tokens: 2000},
					{IssueID: "i-8", Identifier: "ENG-8", Title: "expensive H", CostUSD: 1.5, Tokens: 1500},
				},
			}, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "get_ai_costs",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"days":         7,
		},
	})
	data := toolResult(t, resp)

	if data["total_cost_usd"].(float64) != 45.67 {
		t.Errorf("total_cost_usd = %v, want 45.67", data["total_cost_usd"])
	}
	if data["projected_monthly_usd"].(float64) != 195.78 {
		t.Errorf("projected_monthly_usd = %v, want 195.78", data["projected_monthly_usd"])
	}
	top := data["top_cost_issues"].([]any)
	if len(top) != 5 {
		t.Fatalf("top_cost_issues has %d entries, want 5 (trim to top 5)", len(top))
	}
	first := top[0].(map[string]any)
	if first["identifier"] != "ENG-1" || first["cost_usd"].(float64) != 12.5 {
		t.Errorf("top[0] = %+v", first)
	}
}

func TestMCP_TriageIssue_GracefullyDegradesWhenAIUnavailable(t *testing.T) {
	s := newTestServer(t)
	s.issueStore = &fakeIssueStore{
		getByIDFn: func(_ context.Context, id string) (*model.Issue, error) {
			return &model.Issue{ID: id, WorkspaceID: "ws-1", Title: "x", Description: "y"}, nil
		},
	}
	s.aiEngine = &fakeAIEngine{available: false}

	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "triage_issue",
		"arguments": map[string]any{"issue_id": "i-1"},
	})
	data := toolResult(t, resp)
	if avail, ok := data["ai_available"].(bool); !ok || avail {
		t.Errorf("ai_available = %v, want false", data["ai_available"])
	}
}

func TestMCP_TriageIssue_AppliesSuggestions(t *testing.T) {
	s := newTestServer(t)
	gotUpdates := map[string]any{}
	s.issueStore = &fakeIssueStore{
		getByIDFn: func(_ context.Context, id string) (*model.Issue, error) {
			return &model.Issue{ID: id, WorkspaceID: "ws-1", Title: "Slow", Description: "x"}, nil
		},
		updateFn: func(_ context.Context, _ string, updates map[string]any) (*model.Issue, error) {
			gotUpdates = updates
			return &model.Issue{ID: "i-1"}, nil
		},
	}
	s.aiEngine = &fakeAIEngine{
		available: true,
		triageFn: func(_ context.Context, _ model.Issue) (*ai.TriageResult, error) {
			return &ai.TriageResult{
				SuggestedPriority: model.PriorityHigh,
				SuggestedLabels:   []string{"perf"},
				Summary:           "Perf regression in cache layer",
				Confidence:        0.9,
			}, nil
		},
	}

	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "triage_issue",
		"arguments": map[string]any{
			"issue_id": "i-1",
			"apply":    true,
		},
	})
	data := toolResult(t, resp)
	if avail, _ := data["ai_available"].(bool); !avail {
		t.Errorf("ai_available = %v, want true", data["ai_available"])
	}
	if got := gotUpdates["priority"].(int); got != int(model.PriorityHigh) {
		t.Errorf("priority = %d, want %d", got, int(model.PriorityHigh))
	}
	if gotUpdates["priority"] == nil || gotUpdates["labels"] == nil {
		t.Errorf("apply=true should have set priority + labels, got %v", gotUpdates)
	}
}

func TestMCP_MoveToCycle_UpdatesIssue(t *testing.T) {
	s := newTestServer(t)
	var gotUpdates map[string]any
	s.issueStore = &fakeIssueStore{
		getByIDFn: func(_ context.Context, id string) (*model.Issue, error) {
			return &model.Issue{ID: id, WorkspaceID: "ws-1"}, nil
		},
		updateFn: func(_ context.Context, _ string, updates map[string]any) (*model.Issue, error) {
			gotUpdates = updates
			return &model.Issue{ID: "i-1"}, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "move_to_cycle",
		"arguments": map[string]any{
			"issue_id": "i-1",
			"cycle_id": "cycle-7",
		},
	})
	data := toolResult(t, resp)
	if data["ok"] != true {
		t.Errorf("ok = %v, want true", data["ok"])
	}
	if gotUpdates["cycle_id"] != "cycle-7" {
		t.Errorf("cycle_id update = %v", gotUpdates["cycle_id"])
	}
}

func TestMCP_CreateProject_ReturnsProject(t *testing.T) {
	s := newTestServer(t)
	s.projectStore = &fakeProjectStore{
		createFn: func(_ context.Context, p model.Project) (*model.Project, error) {
			out := p
			out.ID = "p-1"
			out.Status = "active"
			return &out, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "create_project",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"team_id":      "team-1",
			"name":         "Q3 Launch",
			"description":  "the big one",
		},
	})
	data := toolResult(t, resp)
	if data["id"] != "p-1" {
		t.Errorf("id = %v", data["id"])
	}
	if data["name"] != "Q3 Launch" {
		t.Errorf("name = %v", data["name"])
	}
	if data["status"] != "active" {
		t.Errorf("status = %v", data["status"])
	}
}

func TestMCP_ListTeamMembers_ReturnsArray(t *testing.T) {
	s := newTestServer(t)
	s.members = &fakeMembers{
		listFn: func(_ context.Context, ws, team string) ([]model.Member, error) {
			if ws != "ws-1" || team != "team-1" {
				return nil, errors.New("unexpected args")
			}
			return []model.Member{
				{ID: "m-1", Name: "Alice", Email: "a@example.com", Role: "engineer"},
				{ID: "m-2", Name: "Bob", Email: "b@example.com", Role: "manager"},
			}, nil
		},
	}
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "list_team_members",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"team_id":      "team-1",
		},
	})
	data := toolResult(t, resp)
	members := data["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}
	first := members[0].(map[string]any)
	if first["name"] != "Alice" || first["role"] != "engineer" {
		t.Errorf("first = %+v", first)
	}
}

// TestMCP_UnmappedTool_DeniedFailClosed — a tool the authz chokepoint doesn't map resolves
// to ws="" and is DENIED before dispatch. This is the fail-closed property: a new tool
// added to the dispatch switch but not to toolWorkspace cannot become an open surface.
func TestMCP_UnmappedTool_DeniedFailClosed(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name":      "no_such_tool",
		"arguments": map[string]any{},
	})
	if resp.Error == nil {
		t.Fatal("expected a JSON-RPC error for an unmapped tool")
	}
	if resp.Error.Code != rpcErrUnauthorized {
		t.Errorf("error code = %d, want %d (unauthorized — fail-closed deny before dispatch)", resp.Error.Code, rpcErrUnauthorized)
	}
}

func TestMCP_SSEReturnsEndpointEvent(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.HandleSSE(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: endpoint") {
		t.Errorf("missing endpoint event in body:\n%s", body)
	}
	if !strings.Contains(body, `"uri":"/mcp"`) {
		t.Errorf("missing endpoint URI in body:\n%s", body)
	}
}

func TestMCP_MissingRequiredParamReturnsInvalidParamError(t *testing.T) {
	s := newTestServer(t)
	// create_issue without team_id → -32602.
	resp := rpcCall(t, s, "tools/call", map[string]any{
		"name": "create_issue",
		"arguments": map[string]any{
			"workspace_id": "ws-1",
			"title":        "missing team",
		},
	})
	if resp.Error == nil {
		t.Fatal("expected JSON-RPC error for missing param")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
}

func TestMCP_InvalidJSONReturnsParseError(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	s.HandleRPC(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (errors travel as JSON-RPC envelopes)", w.Code)
	}
	var resp rpcResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body should be valid JSON-RPC: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Errorf("error = %+v, want code -32700", resp.Error)
	}
}
