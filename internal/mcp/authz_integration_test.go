package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/ai"
	"github.com/talyvor/track/internal/analytics"
	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/lensintegration"
	"github.com/talyvor/track/internal/mcp"
	"github.com/talyvor/track/internal/project"
	"github.com/talyvor/track/internal/testutil"
)

const secret = "test-gateway-transit-secret-0123456789"

func seedMember(t *testing.T, d *testutil.DB, wsID, email string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO members (workspace_id, name, email, role) VALUES ($1,$2,$3,'member') RETURNING id`,
		wsID, email, email).Scan(&id); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return id
}

func seedActiveCycle(t *testing.T, d *testutil.DB, wsID, teamID string) string {
	t.Helper()
	var id string
	if err := d.Pool.QueryRow(context.Background(),
		`INSERT INTO cycles (team_id, workspace_id, name, number, status, start_date, end_date)
		 VALUES ($1,$2,'Sprint',1,'active', NOW()-interval '1 day', NOW()+interval '7 days') RETURNING id`,
		teamID, wsID).Scan(&id); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	return id
}

// mcpChain wires the REAL T9 (transit proof) + T10 (membership) middleware in front of the
// MCP server — the same stack production uses for /mcp.
func mcpChain(d *testutil.DB) http.Handler {
	issueStore := issue.NewStore(d.Pool)
	srv := mcp.New(
		issueStore,
		project.NewStore(d.Pool),
		cycle.NewStore(d.Pool),
		ai.New(lensintegration.New("", ""), issueStore, d.Pool), // degraded AI (not exercised here)
		analytics.New(d.Pool),
		"test",
	).WithMembersPool(d.Pool)

	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		r.Post("/mcp", srv.HandleRPC)
	})
	return r
}

type rpcResp struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result json.RawMessage `json:"result"`
}

// callTool fires a tools/call through the full chain as `email`. proof="" omits the transit
// proof header (to test the 401 boundary).
func callTool(t *testing.T, h http.Handler, proof, email, name string, args map[string]any) (int, rpcResp) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	if proof != "" {
		req.Header.Set(gatewayauth.HeaderGatewayAuth, proof)
	}
	req.Header.Set(gatewayauth.HeaderUserEmail, email)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var resp rpcResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	return rr.Code, resp
}

// TestMCPAuthz_WorkspaceKeyed_DenyCrossWorkspace — a workspace-keyed tool (list_issues) with
// workspace_id=B by a member of A only → denied. (RED with the chokepoint neutered: returns
// B's issues.)
func TestMCPAuthz_WorkspaceKeyed_DenyCrossWorkspace(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	_ = d.Issue(t, wsB.ID, teamB.ID) // an issue in B
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := mcpChain(d)
	code, resp := callTool(t, h, secret, "x@corp.com", "list_issues", map[string]any{"workspace_id": wsB.ID})
	if code != http.StatusOK {
		t.Fatalf("http = %d, want 200 (JSON-RPC envelope)", code)
	}
	if resp.Error == nil {
		t.Fatalf("member-of-A list_issues on B = ok, want DENY; result=%s", resp.Result)
	}
}

// TestMCPAuthz_IssueKeyed_DenyCrossWorkspace — THE sharp case the uniform-workspace_id
// assumption would miss: get_issue takes an issue_id, not a workspace. A member of A asking
// for B's issue → the chokepoint resolves the issue to workspace B, which A isn't a member
// of → denied. (RED with the chokepoint neutered: returns B's issue.)
func TestMCPAuthz_IssueKeyed_DenyCrossWorkspace(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	issueB := d.Issue(t, wsB.ID, teamB.ID)
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := mcpChain(d)
	_, resp := callTool(t, h, secret, "x@corp.com", "get_issue", map[string]any{"issue_id": issueB.ID})
	if resp.Error == nil {
		t.Fatalf("member-of-A get_issue on B's issue = ok, want DENY; result=%s", resp.Result)
	}
}

// TestMCPAuthz_SprintStatus_DenyCrossWorkspaceTeam — get_sprint_status's GetActive has no
// workspace filter; the chokepoint resolves the team to its workspace, so a member of A
// passing B's team_id is denied (B's sprint never leaks).
func TestMCPAuthz_SprintStatus_DenyCrossWorkspaceTeam(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamB := d.Team(t, wsB.ID)
	seedActiveCycle(t, d, wsB.ID, teamB.ID) // B has a live sprint
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := mcpChain(d)
	_, resp := callTool(t, h, secret, "x@corp.com", "get_sprint_status",
		map[string]any{"workspace_id": wsA.ID, "team_id": teamB.ID}) // A's ws claim, B's team
	if resp.Error == nil {
		t.Fatalf("member-of-A get_sprint_status on B's team = ok, want DENY; result=%s", resp.Result)
	}
}

// TestMCPAuthz_MoveToCycle_CrossWorkspaceCycleRejected — workspace authorized (X moves their
// own issue in A), but the cycle_id is in B. The issue store's EXISTING tenancy rejects the
// cross-workspace cycle ref — asserted here, not rebuilt.
func TestMCPAuthz_MoveToCycle_CrossWorkspaceCycleRejected(t *testing.T) {
	d := testutil.New(t)
	wsA, wsB := d.Workspace(t), d.Workspace(t)
	teamA, teamB := d.Team(t, wsA.ID), d.Team(t, wsB.ID)
	issueA := d.Issue(t, wsA.ID, teamA.ID)
	cycleB := seedActiveCycle(t, d, wsB.ID, teamB.ID)
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := mcpChain(d)
	_, resp := callTool(t, h, secret, "x@corp.com", "move_to_cycle",
		map[string]any{"issue_id": issueA.ID, "cycle_id": cycleB})
	if resp.Error == nil {
		t.Fatalf("move issue A into cycle B = ok, want store tenancy rejection (T5b)")
	}
}

// TestMCPAuthz_Legit_MemberOfA — a member of A on A's workspace → ok.
func TestMCPAuthz_Legit_MemberOfA(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	_ = d.Issue(t, wsA.ID, teamA.ID)
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := mcpChain(d)
	_, resp := callTool(t, h, secret, "x@corp.com", "list_issues", map[string]any{"workspace_id": wsA.ID})
	if resp.Error != nil {
		t.Fatalf("member-of-A list_issues on A = %+v, want ok", resp.Error)
	}
}

// TestMCPAuthz_NoMembership_Deny — a verified user with no membership → denied on any tool.
func TestMCPAuthz_NoMembership_Deny(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := mcpChain(d)
	_, resp := callTool(t, h, secret, "nobody@corp.com", "list_issues", map[string]any{"workspace_id": wsA.ID})
	if resp.Error == nil {
		t.Fatal("no-membership user = ok, want DENY")
	}
}

// TestMCPAuthz_NoTransitProof_401 — without a valid x-gateway-auth, gatewayauth rejects with
// 401 BEFORE HandleRPC runs any tool.
func TestMCPAuthz_NoTransitProof_401(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	seedMember(t, d, wsA.ID, "x@corp.com")

	h := mcpChain(d)
	code, _ := callTool(t, h, "", "x@corp.com", "list_issues", map[string]any{"workspace_id": wsA.ID})
	if code != http.StatusUnauthorized {
		t.Fatalf("no transit proof = %d, want 401", code)
	}
	codeWrong, _ := callTool(t, h, "wrong-secret-but-long-enough", "x@corp.com", "list_issues", map[string]any{"workspace_id": wsA.ID})
	if codeWrong != http.StatusUnauthorized {
		t.Fatalf("forged transit proof = %d, want 401", codeWrong)
	}
}

// TestMCPAuthz_Actor_ResolvedMember — create_issue records the resolved member.id as the
// creator, not the "agent" constant.
func TestMCPAuthz_Actor_ResolvedMember(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	teamA := d.Team(t, wsA.ID)
	memberID := seedMember(t, d, wsA.ID, "x@corp.com")

	h := mcpChain(d)
	_, resp := callTool(t, h, secret, "x@corp.com", "create_issue", map[string]any{
		"workspace_id": wsA.ID, "team_id": teamA.ID, "title": "via mcp",
	})
	if resp.Error != nil {
		t.Fatalf("create_issue = %+v, want ok", resp.Error)
	}
	var creator string
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT creator_id FROM issues WHERE workspace_id=$1 AND title='via mcp'`, wsA.ID).Scan(&creator); err != nil {
		t.Fatal(err)
	}
	if creator != memberID {
		t.Fatalf("creator_id = %q, want the resolved member.id %q (not 'agent')", creator, memberID)
	}
}
