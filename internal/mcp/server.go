// Package mcp implements Talyvor Track's Model Context Protocol server.
//
// Agents (Claude Code, Codex, custom agents) call this over HTTP +
// SSE to create / read / update issues, run AI triage, and pull AI
// cost telemetry without leaving the terminal. The protocol shape
// matches Talyvor Lens's MCP server so a single client config can
// reach both servers.
//
// All twelve tools are read-only at the protocol layer (JSON-RPC 2.0)
// — they delegate to the existing Track stores and engines, so any
// rule those packages enforce (per-team issue numbering, status
// transition semantics, auto-complete stamping) applies here too.
//
// The get_ai_costs tool is Track's unique surface: no other tracker
// exposes per-workspace LLM spend to agents through MCP, and that
// gap is the entire reason a CFO might mandate Track over Linear.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/talyvor/track/internal/ai"
	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/project"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "talyvor-track"
	ssePingInterval = 30 * time.Second

	rpcErrParse        = -32700
	rpcErrMethodNotFnd = -32601
	rpcErrInvalidParam = -32602
	rpcErrInternal     = -32603
	rpcErrUnauthorized = -32001 // server-defined (JSON-RPC -32000..-32099): caller not authorized for the workspace
)

// ─── internal interfaces ─────────────────────────────────────
//
// Each interface is the subset of the corresponding Track package's
// public surface this server actually exercises. The concrete
// *issue.Store / *project.Store / *cycle.Store / *ai.Engine /
// *analytics.Engine values all satisfy these by virtue of having the
// matching method sets — no adapters needed at the wiring layer.

type issueStoreIface interface {
	Create(ctx context.Context, i model.Issue) (*model.Issue, error)
	GetByID(ctx context.Context, id string) (*model.Issue, error)
	GetByIdentifier(ctx context.Context, identifier string) (*model.Issue, error)
	List(ctx context.Context, filter issue.IssueFilter) ([]model.Issue, error)
	Update(ctx context.Context, id, workspaceID string, updates map[string]any) (*model.Issue, error)
	Search(ctx context.Context, workspaceID, query string, limit int) ([]model.Issue, error)
	CreateComment(ctx context.Context, c model.Comment) (*model.Comment, error)
}

type projectStoreIface interface {
	Create(ctx context.Context, p model.Project) (*model.Project, error)
}

type cycleStoreIface interface {
	GetActive(ctx context.Context, teamID string) (*model.Cycle, error)
	GetProgress(ctx context.Context, cycleID string) (*cycle.CycleProgress, error)
}

type aiEngineIface interface {
	IsAvailable() bool
	TriageIssue(ctx context.Context, i model.Issue) (*ai.TriageResult, error)
}

type analyticsIface interface {
	GetAICostTrends(ctx context.Context, workspaceID string, days int) (*analytics.AICostTrends, error)
}

// memberLister isolates the one members-table query out of the
// otherwise pool-free Server. main.go injects a pgx-backed lister via
// WithMembersPool; tests supply a closure-based fake.
type memberLister interface {
	ListMembers(ctx context.Context, workspaceID, teamID string) ([]model.Member, error)
	// WorkspaceOfTeam resolves a team to its workspace, for the authz chokepoint
	// (get_sprint_status acts on a team whose GetActive query has no workspace filter).
	WorkspaceOfTeam(ctx context.Context, teamID string) (string, error)
}

type Server struct {
	issueStore   issueStoreIface
	projectStore projectStoreIface
	cycleStore   cycleStoreIface
	aiEngine     aiEngineIface
	analytics    analyticsIface
	members      memberLister
	version      string
}

// New is the production constructor. The concrete store / engine
// pointers satisfy the package-internal interfaces; the typed
// signature stays clean for callers in cmd/track.
func New(
	issueStore *issue.Store,
	projectStore *project.Store,
	cycleStore *cycle.Store,
	aiEngine *ai.Engine,
	analyticsEngine *analytics.Engine,
	version string,
) *Server {
	return newServer(
		issueStore, projectStore, cycleStore, aiEngine, analyticsEngine,
		nopMembers{}, // configured later via WithMembersPool
		version,
	)
}

// newServer is the test seam — it accepts the interface types
// directly so unit tests can pass small closure-backed fakes.
func newServer(
	is issueStoreIface,
	ps projectStoreIface,
	cs cycleStoreIface,
	ae aiEngineIface,
	an analyticsIface,
	ml memberLister,
	version string,
) *Server {
	return &Server{
		issueStore:   is,
		projectStore: ps,
		cycleStore:   cs,
		aiEngine:     ae,
		analytics:    an,
		members:      ml,
		version:      version,
	}
}

// WithMembersPool attaches a pgx pool the server can use for the
// list_team_members tool. Returning *Server keeps the wiring fluent
// (mcp.New(...).WithMembersPool(pool)).
func (s *Server) WithMembersPool(pool *pgxpool.Pool) *Server {
	if pool != nil {
		s.members = &membersStore{pool: pool}
	}
	return s
}

// ─── JSON-RPC 2.0 envelopes ─────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// invalidParamErr is the sentinel a tool returns when its required
// arguments are missing — the dispatcher then maps it to -32602
// instead of the generic -32603 internal error. Other errors stay
// internal so we don't accidentally classify upstream failures as
// caller-input bugs.
type invalidParamErr struct{ msg string }

func (e *invalidParamErr) Error() string { return e.msg }

func badParam(msg string) error { return &invalidParamErr{msg: msg} }

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// ─── HTTP handlers ──────────────────────────────────────────

// HandleRPC dispatches one JSON-RPC request. MCP clients open this
// endpoint per call; long-lived streaming happens at /mcp/sse.
func (s *Server) HandleRPC(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, rpcErrParse, "Parse error")
		return
	}
	switch req.Method {
	case "initialize":
		s.writeResult(w, req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    serverName,
				"version": s.version,
			},
		})
	case "tools/list":
		s.writeResult(w, req.ID, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		s.handleToolsCall(w, r.Context(), req.ID, req.Params)
	default:
		s.writeError(w, req.ID, rpcErrMethodNotFnd, "method not found: "+req.Method)
	}
}

// HandleSSE keeps an SSE connection open and periodically pings the
// client. The opening `endpoint` event tells the client where to send
// JSON-RPC traffic — clients use it to wake up after a network blip.
func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: {\"uri\":\"/mcp\"}\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	ticker := time.NewTicker(ssePingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// ─── tool catalogue ─────────────────────────────────────────

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "create_issue",
			"description": "Create a new issue in a team's queue. Returns the assigned identifier (e.g. ENG-42) and a URL that opens the issue in Track.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string", "description": "Workspace the issue belongs to."},
					"team_id":      map[string]any{"type": "string", "description": "Team that will own the issue."},
					"title":        map[string]any{"type": "string", "description": "Short summary; appears in lists."},
					"description":  map[string]any{"type": "string", "description": "Markdown body; optional."},
					"priority":     map[string]any{"type": "integer", "minimum": 0, "maximum": 4, "description": "0=none, 1=urgent, 2=high, 3=medium, 4=low."},
					"labels":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Free-form tags."},
					"project_id":   map[string]any{"type": "string", "description": "Optional project the issue rolls up into."},
				},
				"required": []string{"workspace_id", "team_id", "title"},
			},
		},
		{
			"name":        "update_issue",
			"description": "Patch one or more fields of an existing issue. Only the fields you pass are touched. Status transitions to 'done' auto-stamp completed_at.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"issue_id":    map[string]any{"type": "string"},
					"title":       map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"status":      map[string]any{"type": "string", "enum": []string{"backlog", "todo", "in_progress", "in_review", "done", "cancelled"}},
					"priority":    map[string]any{"type": "integer", "minimum": 0, "maximum": 4},
					"assignee_id": map[string]any{"type": "string"},
				},
				"required": []string{"issue_id"},
			},
		},
		{
			"name":        "get_issue",
			"description": "Fetch a single issue by ID or human identifier (e.g. ENG-42). Includes ai_cost_usd and ai_tokens accrued via Talyvor Lens.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"issue_id":   map[string]any{"type": "string", "description": "Internal UUID-shaped ID."},
					"identifier": map[string]any{"type": "string", "description": "Human identifier such as ENG-42."},
				},
				"required": []string{},
			},
		},
		{
			"name":        "list_issues",
			"description": "List issues in a workspace, optionally filtered by team, status, or assignee. Default 20, max 100.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string"},
					"team_id":      map[string]any{"type": "string"},
					"status":       map[string]any{"type": "string"},
					"assignee_id":  map[string]any{"type": "string"},
					"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 20},
				},
				"required": []string{"workspace_id"},
			},
		},
		{
			"name":        "search_issues",
			"description": "Full-text search over issue titles and descriptions. Returns up to `limit` matches (default 10) ranked by recency.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string"},
					"query":        map[string]any{"type": "string", "description": "Free-text query. Supports quoted phrases and -exclusions via websearch syntax."},
					"limit":        map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
				},
				"required": []string{"workspace_id", "query"},
			},
		},
		{
			"name":        "add_comment",
			"description": "Add a markdown comment to an issue. author_id defaults to 'agent' so agent-generated commentary is easy to filter in the UI.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"issue_id":  map[string]any{"type": "string"},
					"body":      map[string]any{"type": "string", "description": "Markdown body."},
					"author_id": map[string]any{"type": "string", "default": "agent"},
				},
				"required": []string{"issue_id", "body"},
			},
		},
		{
			"name":        "get_sprint_status",
			"description": "Returns the active cycle for a team with progress (completed / in-progress) and AI cost accumulated this sprint.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string"},
					"team_id":      map[string]any{"type": "string"},
				},
				"required": []string{"workspace_id", "team_id"},
			},
		},
		{
			"name":        "triage_issue",
			"description": "Run AI triage against an issue: suggests priority, labels, and a one-line summary. Pass apply=true to auto-apply the suggestions to the issue.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"issue_id": map[string]any{"type": "string"},
					"apply":    map[string]any{"type": "boolean", "default": false, "description": "When true, apply the suggested priority and labels to the issue."},
				},
				"required": []string{"issue_id"},
			},
		},
		{
			"name":        "get_ai_costs",
			"description": "Workspace LLM cost breakdown for the last N days (default 7) via Talyvor Lens: total spend, top-5 most expensive issues, and projected monthly spend. UNIQUE to Track — no other tracker exposes AI cost data through MCP.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string"},
					"days":         map[string]any{"type": "integer", "minimum": 1, "maximum": 365, "default": 7},
				},
				"required": []string{"workspace_id"},
			},
		},
		{
			"name":        "move_to_cycle",
			"description": "Move an issue into a different cycle (sprint). Use list_issues with cycle_id filters to verify the destination is valid.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"issue_id": map[string]any{"type": "string"},
					"cycle_id": map[string]any{"type": "string"},
				},
				"required": []string{"issue_id", "cycle_id"},
			},
		},
		{
			"name":        "create_project",
			"description": "Create a project (named effort grouping issues across a window) for a team. Identifier is derived from the name.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string"},
					"team_id":      map[string]any{"type": "string"},
					"name":         map[string]any{"type": "string"},
					"description":  map[string]any{"type": "string"},
				},
				"required": []string{"workspace_id", "team_id", "name"},
			},
		},
		{
			"name":        "list_team_members",
			"description": "List members of a workspace, optionally restricted to a team. Returns id, name, email, avatar_url, and role.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string"},
					"team_id":      map[string]any{"type": "string", "description": "Optional. Omit to list every member in the workspace."},
				},
				"required": []string{"workspace_id"},
			},
		},
	}
}

// ─── tools/call dispatch ────────────────────────────────────

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(w http.ResponseWriter, ctx context.Context, id, paramsRaw json.RawMessage) {
	var params toolCallParams
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		s.writeError(w, id, rpcErrInvalidParam, "Invalid params: "+err.Error())
		return
	}

	// ── authz chokepoint ───────────────────────────────────────────────────────────
	// Every tool's acted-on workspace is authorized HERE, once, before dispatch. The
	// workspace is the workspace_id argument (workspace-keyed tools) or is resolved from
	// the object the tool touches (issue-keyed tools -> the issue's workspace;
	// get_sprint_status -> the team's workspace) so authorizing the workspace also covers
	// the secondary id. Fail-closed by construction: an unmapped/new tool, a missing
	// object, or a resolution error yields ws="" and is denied — a new tool cannot be an
	// open surface. The resolved member becomes the actor (authz.MemberID).
	// NOTE: the T12 semgrep lock does NOT cover this body/tool-arg form — these authz
	// tests are MCP's guard.
	ws, rerr := s.toolWorkspace(ctx, params.Name, params.Arguments)
	if rerr != nil || ws == "" {
		s.writeError(w, id, rpcErrUnauthorized, "not authorized for the requested workspace")
		return
	}
	m, ok := authz.AuthorizeWorkspace(ctx, ws)
	if !ok {
		s.writeError(w, id, rpcErrUnauthorized, "not a member of this workspace")
		return
	}
	ctx = authz.WithAuthorized(ctx, m.WorkspaceID, m.MemberID)
	// ───────────────────────────────────────────────────────────────────────────────

	var (
		result any
		err    error
	)
	switch params.Name {
	case "create_issue":
		result, err = s.toolCreateIssue(ctx, params.Arguments)
	case "update_issue":
		result, err = s.toolUpdateIssue(ctx, params.Arguments)
	case "get_issue":
		result, err = s.toolGetIssue(ctx, params.Arguments)
	case "list_issues":
		result, err = s.toolListIssues(ctx, params.Arguments)
	case "search_issues":
		result, err = s.toolSearchIssues(ctx, params.Arguments)
	case "add_comment":
		result, err = s.toolAddComment(ctx, params.Arguments)
	case "get_sprint_status":
		result, err = s.toolGetSprintStatus(ctx, params.Arguments)
	case "triage_issue":
		result, err = s.toolTriageIssue(ctx, params.Arguments)
	case "get_ai_costs":
		result, err = s.toolGetAICosts(ctx, params.Arguments)
	case "move_to_cycle":
		result, err = s.toolMoveToCycle(ctx, params.Arguments)
	case "create_project":
		result, err = s.toolCreateProject(ctx, params.Arguments)
	case "list_team_members":
		result, err = s.toolListTeamMembers(ctx, params.Arguments)
	default:
		s.writeError(w, id, rpcErrMethodNotFnd, "unknown tool: "+params.Name)
		return
	}
	if err != nil {
		var bad *invalidParamErr
		if errors.As(err, &bad) {
			s.writeError(w, id, rpcErrInvalidParam, bad.Error())
			return
		}
		s.writeError(w, id, rpcErrInternal, err.Error())
		return
	}

	body, mErr := json.Marshal(result)
	if mErr != nil {
		s.writeError(w, id, rpcErrInternal, "result marshal: "+mErr.Error())
		return
	}
	s.writeResult(w, id, map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": string(body),
		}},
	})
}

// toolWorkspace computes the workspace a tool call acts on, for the authz chokepoint.
// Workspace-keyed tools carry it as an argument; issue-keyed tools and get_sprint_status
// derive it from the object they touch, so authorizing the workspace also covers the
// secondary id (a cross-workspace issue_id or team_id resolves to a workspace the caller
// is not a member of, and is denied). An unmapped tool returns "" → denied (fail-closed).
func (s *Server) toolWorkspace(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "create_issue", "list_issues", "search_issues", "get_ai_costs", "create_project", "list_team_members":
		var a struct {
			WorkspaceID string `json:"workspace_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		return a.WorkspaceID, nil
	case "get_sprint_status":
		// The sprint belongs to the team's workspace — resolve it so a cross-workspace
		// team_id (GetActive has no workspace filter) is denied, not leaked.
		var a struct {
			TeamID string `json:"team_id"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		if a.TeamID == "" {
			return "", nil
		}
		return s.members.WorkspaceOfTeam(ctx, a.TeamID)
	case "get_issue", "update_issue", "add_comment", "triage_issue", "move_to_cycle":
		var a struct {
			IssueID    string `json:"issue_id"`
			Identifier string `json:"identifier"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		return s.workspaceForIssue(ctx, a.IssueID, a.Identifier)
	default:
		return "", nil // unmapped/new tool → deny
	}
}

// workspaceForIssue resolves an issue (by id or human identifier) to its workspace. A
// not-found issue or any lookup error yields "" → the chokepoint denies (no existence
// leak, fail-closed).
func (s *Server) workspaceForIssue(ctx context.Context, issueID, identifier string) (string, error) {
	var (
		iss *model.Issue
		err error
	)
	switch {
	case issueID != "":
		iss, err = s.issueStore.GetByID(ctx, issueID)
	case identifier != "":
		iss, err = s.issueStore.GetByIdentifier(ctx, identifier)
	default:
		return "", nil
	}
	if err != nil || iss == nil {
		return "", nil // treat not-found / error as deny
	}
	return iss.WorkspaceID, nil
}

// ─── tool 1: create_issue ───────────────────────────────────

func (s *Server) toolCreateIssue(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		WorkspaceID string   `json:"workspace_id"`
		TeamID      string   `json:"team_id"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Priority    int      `json:"priority"`
		Labels      []string `json:"labels"`
		ProjectID   string   `json:"project_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.WorkspaceID == "" {
		return nil, badParam("workspace_id required")
	}
	if in.TeamID == "" {
		return nil, badParam("team_id required")
	}
	if in.Title == "" {
		return nil, badParam("title required")
	}
	if in.Priority < 0 || in.Priority > 4 {
		return nil, badParam("priority must be 0..4")
	}

	actor, _ := authz.MemberID(ctx) // resolved member (the chokepoint authorized it); replaces the "agent" constant
	new := model.Issue{
		WorkspaceID: in.WorkspaceID,
		TeamID:      in.TeamID,
		Title:       in.Title,
		Description: in.Description,
		Priority:    model.IssuePriority(in.Priority),
		Labels:      in.Labels,
		CreatorID:   actor,
		Status:      model.StatusTodo,
	}
	if in.ProjectID != "" {
		pid := in.ProjectID
		new.ProjectID = &pid
	}

	out, err := s.issueStore.Create(ctx, new)
	if err != nil {
		return nil, fmt.Errorf("create_issue: %w", err)
	}
	return map[string]any{
		"id":         out.ID,
		"identifier": out.Identifier,
		"title":      out.Title,
		"status":     string(out.Status),
		"priority":   int(out.Priority),
		"url":        issueURL(out),
	}, nil
}

// ─── tool 2: update_issue ───────────────────────────────────

func (s *Server) toolUpdateIssue(ctx context.Context, args json.RawMessage) (any, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	idRaw, ok := raw["issue_id"]
	if !ok {
		return nil, badParam("issue_id required")
	}
	var id string
	if err := json.Unmarshal(idRaw, &id); err != nil || id == "" {
		return nil, badParam("issue_id required")
	}

	// Whitelist incoming keys — Update inside the store also enforces
	// its own allowlist, but checking here lets us return a clear
	// "no editable fields" error instead of a silently-empty update.
	updates := map[string]any{}
	type spec struct {
		key  string
		kind string // "string" or "int"
	}
	for _, f := range []spec{
		{"title", "string"},
		{"description", "string"},
		{"status", "string"},
		{"assignee_id", "string"},
		{"priority", "int"},
	} {
		v, ok := raw[f.key]
		if !ok {
			continue
		}
		switch f.kind {
		case "string":
			var sv string
			if err := json.Unmarshal(v, &sv); err != nil {
				return nil, badParam(f.key + " must be a string")
			}
			updates[f.key] = sv
		case "int":
			var iv int
			if err := json.Unmarshal(v, &iv); err != nil {
				return nil, badParam(f.key + " must be an integer")
			}
			updates[f.key] = iv
		}
	}

	wsID, _ := authz.WorkspaceID(ctx) // SEC-5: authorized workspace (set by the tools/call chokepoint)
	out, err := s.issueStore.Update(ctx, id, wsID, updates)
	if err != nil {
		return nil, fmt.Errorf("update_issue: %w", err)
	}
	return summariseIssue(out), nil
}

// ─── tool 3: get_issue ──────────────────────────────────────

func (s *Server) toolGetIssue(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		IssueID    string `json:"issue_id"`
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.IssueID == "" && in.Identifier == "" {
		return nil, badParam("issue_id or identifier required")
	}
	var (
		out *model.Issue
		err error
	)
	if in.IssueID != "" {
		out, err = s.issueStore.GetByID(ctx, in.IssueID)
	} else {
		out, err = s.issueStore.GetByIdentifier(ctx, in.Identifier)
	}
	if err != nil {
		return nil, fmt.Errorf("get_issue: %w", err)
	}
	return fullIssue(out), nil
}

// ─── tool 4: list_issues ────────────────────────────────────

func (s *Server) toolListIssues(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		TeamID      string `json:"team_id"`
		Status      string `json:"status"`
		AssigneeID  string `json:"assignee_id"`
		Limit       int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.WorkspaceID == "" {
		return nil, badParam("workspace_id required")
	}
	if in.Limit <= 0 {
		in.Limit = 20
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	issues, err := s.issueStore.List(ctx, issue.IssueFilter{
		WorkspaceID: in.WorkspaceID,
		TeamID:      in.TeamID,
		Status:      in.Status,
		AssigneeID:  in.AssigneeID,
		Limit:       in.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list_issues: %w", err)
	}
	out := make([]map[string]any, 0, len(issues))
	for i := range issues {
		out = append(out, summariseIssue(&issues[i]))
	}
	return map[string]any{"issues": out, "count": len(out)}, nil
}

// ─── tool 5: search_issues ──────────────────────────────────

func (s *Server) toolSearchIssues(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		Query       string `json:"query"`
		Limit       int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.WorkspaceID == "" {
		return nil, badParam("workspace_id required")
	}
	if in.Query == "" {
		return nil, badParam("query required")
	}
	if in.Limit <= 0 {
		in.Limit = 10
	}
	results, err := s.issueStore.Search(ctx, in.WorkspaceID, in.Query, in.Limit)
	if err != nil {
		return nil, fmt.Errorf("search_issues: %w", err)
	}
	out := make([]map[string]any, 0, len(results))
	for i := range results {
		out = append(out, summariseIssue(&results[i]))
	}
	return map[string]any{"issues": out, "count": len(out)}, nil
}

// ─── tool 6: add_comment ────────────────────────────────────

func (s *Server) toolAddComment(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		IssueID  string `json:"issue_id"`
		Body     string `json:"body"`
		AuthorID string `json:"author_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.IssueID == "" {
		return nil, badParam("issue_id required")
	}
	if in.Body == "" {
		return nil, badParam("body required")
	}
	// The comment author is the resolved, gateway-verified member — never a caller-supplied
	// author_id (that would let an agent spoof authorship). The author_id argument is ignored.
	in.AuthorID, _ = authz.MemberID(ctx)
	out, err := s.issueStore.CreateComment(ctx, model.Comment{
		IssueID:  in.IssueID,
		Body:     in.Body,
		AuthorID: in.AuthorID,
	})
	if err != nil {
		return nil, fmt.Errorf("add_comment: %w", err)
	}
	return map[string]any{
		"id":         out.ID,
		"issue_id":   out.IssueID,
		"author_id":  out.AuthorID,
		"body":       out.Body,
		"created_at": out.CreatedAt.UTC().Format(time.RFC3339),
	}, nil
}

// ─── tool 7: get_sprint_status ──────────────────────────────

func (s *Server) toolGetSprintStatus(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		TeamID      string `json:"team_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.WorkspaceID == "" {
		return nil, badParam("workspace_id required")
	}
	if in.TeamID == "" {
		return nil, badParam("team_id required")
	}
	active, err := s.cycleStore.GetActive(ctx, in.TeamID)
	if err != nil {
		// No active cycle is not an error condition — agents should
		// see a clear "no active sprint" signal instead of a 500.
		return map[string]any{
			"active":  false,
			"team_id": in.TeamID,
		}, nil
	}
	progress, err := s.cycleStore.GetProgress(ctx, active.ID)
	if err != nil {
		return nil, fmt.Errorf("get_sprint_status: %w", err)
	}
	return map[string]any{
		"active":         true,
		"cycle_id":       active.ID,
		"cycle_name":     active.Name,
		"start_date":     active.StartDate.UTC().Format(time.RFC3339),
		"end_date":       active.EndDate.UTC().Format(time.RFC3339),
		"total_issues":   progress.TotalIssues,
		"completed":      progress.Completed,
		"in_progress":    progress.InProgress,
		"completion_pct": progress.CompletionPct,
		"ai_cost_usd":    progress.TotalAICostUSD,
	}, nil
}

// ─── tool 8: triage_issue ───────────────────────────────────

func (s *Server) toolTriageIssue(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		IssueID string `json:"issue_id"`
		Apply   bool   `json:"apply"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.IssueID == "" {
		return nil, badParam("issue_id required")
	}
	if !s.aiEngine.IsAvailable() {
		// Graceful degradation: agents see ai_available=false and can
		// route around it instead of receiving a 5xx.
		return map[string]any{
			"ai_available": false,
			"issue_id":     in.IssueID,
			"reason":       "Lens not configured or AI engine offline",
		}, nil
	}
	iss, err := s.issueStore.GetByID(ctx, in.IssueID)
	if err != nil {
		return nil, fmt.Errorf("triage_issue: %w", err)
	}
	tri, err := s.aiEngine.TriageIssue(ctx, *iss)
	if err != nil {
		return nil, fmt.Errorf("triage_issue: %w", err)
	}

	applied := false
	if in.Apply {
		updates := map[string]any{
			"priority": int(tri.SuggestedPriority),
		}
		if len(tri.SuggestedLabels) > 0 {
			updates["labels"] = tri.SuggestedLabels
		}
		wsID, _ := authz.WorkspaceID(ctx) // SEC-5: authorized workspace (chokepoint)
		if _, err := s.issueStore.Update(ctx, iss.ID, wsID, updates); err != nil {
			return nil, fmt.Errorf("triage_issue apply: %w", err)
		}
		applied = true
	}

	return map[string]any{
		"ai_available":       true,
		"issue_id":           iss.ID,
		"suggested_priority": int(tri.SuggestedPriority),
		"suggested_labels":   nonNilStrings(tri.SuggestedLabels),
		"summary":            tri.Summary,
		"confidence":         tri.Confidence,
		"applied":            applied,
	}, nil
}

// ─── tool 9: get_ai_costs ───────────────────────────────────
//
// The talyvor-track unique competitive surface. Linear's MCP doesn't
// expose this data at all — agents using Linear must guess at LLM
// spend or scrape the Lens dashboard manually. Track surfaces it
// directly so an agent can answer "should I run another duplicate
// detection pass this week?" with a real budget check.
func (s *Server) toolGetAICosts(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		Days        int    `json:"days"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.WorkspaceID == "" {
		return nil, badParam("workspace_id required")
	}
	if in.Days <= 0 {
		in.Days = 7
	}
	trends, err := s.analytics.GetAICostTrends(ctx, in.WorkspaceID, in.Days)
	if err != nil {
		return nil, fmt.Errorf("get_ai_costs: %w", err)
	}
	top := trends.TopCostIssues
	if len(top) > 5 {
		top = top[:5]
	}
	topOut := make([]map[string]any, 0, len(top))
	for _, t := range top {
		topOut = append(topOut, map[string]any{
			"issue_id":   t.IssueID,
			"identifier": t.Identifier,
			"title":      t.Title,
			"cost_usd":   t.CostUSD,
			"tokens":     t.Tokens,
		})
	}
	return map[string]any{
		"workspace_id":          in.WorkspaceID,
		"period_days":           in.Days,
		"total_cost_usd":        trends.TotalCostUSD,
		"projected_monthly_usd": trends.ProjectedMonthly,
		"avg_cost_per_issue":    trends.AvgCostPerIssue,
		"top_cost_issues":       topOut,
		"source":                "talyvor-lens",
	}, nil
}

// ─── tool 10: move_to_cycle ─────────────────────────────────

func (s *Server) toolMoveToCycle(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		IssueID string `json:"issue_id"`
		CycleID string `json:"cycle_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.IssueID == "" {
		return nil, badParam("issue_id required")
	}
	if in.CycleID == "" {
		return nil, badParam("cycle_id required")
	}
	wsID, _ := authz.WorkspaceID(ctx) // SEC-5: authorized workspace (chokepoint)
	if _, err := s.issueStore.Update(ctx, in.IssueID, wsID, map[string]any{
		"cycle_id": in.CycleID,
	}); err != nil {
		return nil, fmt.Errorf("move_to_cycle: %w", err)
	}
	return map[string]any{
		"ok":       true,
		"issue_id": in.IssueID,
		"cycle_id": in.CycleID,
	}, nil
}

// ─── tool 11: create_project ────────────────────────────────

func (s *Server) toolCreateProject(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		TeamID      string `json:"team_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.WorkspaceID == "" {
		return nil, badParam("workspace_id required")
	}
	if in.TeamID == "" {
		return nil, badParam("team_id required")
	}
	if in.Name == "" {
		return nil, badParam("name required")
	}
	out, err := s.projectStore.Create(ctx, model.Project{
		WorkspaceID: in.WorkspaceID,
		TeamID:      in.TeamID,
		Name:        in.Name,
		Identifier:  slugify(in.Name),
		Description: in.Description,
	})
	if err != nil {
		return nil, fmt.Errorf("create_project: %w", err)
	}
	return map[string]any{
		"id":         out.ID,
		"name":       out.Name,
		"identifier": out.Identifier,
		"status":     out.Status,
	}, nil
}

// ─── tool 12: list_team_members ─────────────────────────────

func (s *Server) toolListTeamMembers(ctx context.Context, args json.RawMessage) (any, error) {
	var in struct {
		WorkspaceID string `json:"workspace_id"`
		TeamID      string `json:"team_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, badParam("invalid arguments: " + err.Error())
	}
	if in.WorkspaceID == "" {
		return nil, badParam("workspace_id required")
	}
	members, err := s.members.ListMembers(ctx, in.WorkspaceID, in.TeamID)
	if err != nil {
		return nil, fmt.Errorf("list_team_members: %w", err)
	}
	out := make([]map[string]any, 0, len(members))
	for _, m := range members {
		out = append(out, map[string]any{
			"id":         m.ID,
			"name":       m.Name,
			"email":      m.Email,
			"avatar_url": m.AvatarURL,
			"role":       m.Role,
		})
	}
	return map[string]any{"members": out, "count": len(out)}, nil
}

// ─── helpers ────────────────────────────────────────────────

// issueURL builds the in-app deep-link for an issue. The base is
// fixed; if you self-host Track at a different origin, change this
// in one place.
func issueURL(i *model.Issue) string {
	return "/issues/" + i.Identifier
}

func summariseIssue(i *model.Issue) map[string]any {
	out := map[string]any{
		"id":          i.ID,
		"identifier":  i.Identifier,
		"title":       i.Title,
		"status":      string(i.Status),
		"priority":    int(i.Priority),
		"ai_cost_usd": i.AICostUSD,
	}
	if i.AssigneeID != nil {
		out["assignee_id"] = *i.AssigneeID
	}
	return out
}

func fullIssue(i *model.Issue) map[string]any {
	out := summariseIssue(i)
	out["workspace_id"] = i.WorkspaceID
	out["team_id"] = i.TeamID
	out["description"] = i.Description
	out["ai_tokens"] = i.AITokens
	out["lens_feature"] = i.LensFeature
	out["labels"] = nonNilStrings(i.Labels)
	out["url"] = issueURL(i)
	if i.ProjectID != nil {
		out["project_id"] = *i.ProjectID
	}
	if i.CycleID != nil {
		out["cycle_id"] = *i.CycleID
	}
	return out
}

// nonNilStrings returns an empty slice for nil so JSON encodes `[]`
// rather than `null` — easier for MCP clients to consume.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// slugify derives a short uppercase identifier from a project name.
// "Q3 Launch" → "Q3-LAUNCH". The store enforces uniqueness; collisions
// surface as create errors and the caller can re-submit with a more
// specific name.
func slugify(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r
		case r >= 'a' && r <= 'z':
			return r - 32
		case r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '-' || r == '_':
			return '-'
		default:
			return -1
		}
	}, strings.TrimSpace(name))
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "PROJECT"
	}
	if len(cleaned) > 32 {
		cleaned = cleaned[:32]
	}
	return cleaned
}

// ─── members store (pgx-backed) ─────────────────────────────

type membersStore struct {
	pool *pgxpool.Pool
}

// WorkspaceOfTeam returns the workspace a team belongs to (teams.workspace_id); a missing
// team yields an error, which the authz chokepoint treats as a denial (fail-closed).
func (m *membersStore) WorkspaceOfTeam(ctx context.Context, teamID string) (string, error) {
	var ws string
	if err := m.pool.QueryRow(ctx, `SELECT workspace_id FROM teams WHERE id = $1`, teamID).Scan(&ws); err != nil {
		return "", err
	}
	return ws, nil
}

const memberCols = `id, workspace_id, name, email, avatar_url, role, created_at`

// ListMembers returns workspace members, optionally filtered to those
// assigned at least one open issue on the team (Track has no formal
// team-membership table — assignment history is the de-facto link).
func (m *membersStore) ListMembers(ctx context.Context, workspaceID, teamID string) ([]model.Member, error) {
	if m.pool == nil {
		return nil, errors.New("members: pool not configured")
	}
	var (
		rows pgx.Rows
		err  error
	)
	if teamID == "" {
		rows, err = m.pool.Query(ctx,
			`SELECT `+memberCols+` FROM members WHERE workspace_id = $1 ORDER BY name`,
			workspaceID,
		)
	} else {
		rows, err = m.pool.Query(ctx,
			`SELECT DISTINCT `+addPrefix(memberCols, "m.")+`
            FROM members m
            JOIN issues i ON i.assignee_id = m.id
            WHERE m.workspace_id = $1 AND i.team_id = $2
            ORDER BY m.name`,
			workspaceID, teamID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("members: list: %w", err)
	}
	defer rows.Close()
	var out []model.Member
	for rows.Next() {
		var mem model.Member
		if err := rows.Scan(&mem.ID, &mem.WorkspaceID, &mem.Name, &mem.Email, &mem.AvatarURL, &mem.Role, &mem.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, mem)
	}
	return out, rows.Err()
}

// addPrefix rewrites the comma-separated column list into a
// table-qualified version (`name` → `m.name`) so the joined SELECT
// doesn't trip on the duplicate-column projection when issues also
// has an `id`. Keeping the column list as one constant means new
// member fields only need to land in `memberCols`.
func addPrefix(cols, prefix string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// nopMembers stands in until WithMembersPool wires a real pool. Tools
// that depend on it return a clear "members not configured" error
// rather than crashing on a nil pointer.
type nopMembers struct{}

func (nopMembers) ListMembers(_ context.Context, _, _ string) ([]model.Member, error) {
	return nil, errors.New("members store not configured (call WithMembersPool)")
}

func (nopMembers) WorkspaceOfTeam(_ context.Context, _ string) (string, error) {
	return "", errors.New("members store not configured (call WithMembersPool)")
}

// satisfy unused-import lint when membersStore happens to not be
// instantiated by tests (pgconn is brought in for symmetry with the
// other Track packages' pgxDB interfaces).
var _ = pgconn.CommandTag{}
