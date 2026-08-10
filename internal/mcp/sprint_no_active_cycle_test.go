package mcp_test

// sprint_no_active_cycle_test.go — W3.4 / tab-7c15.
//
// THE DEFECT: `get_sprint_status` dereferenced a nil cycle.
//
// cycle.Store.GetActive returns (nil, nil) when a team has no active cycle — its own doc comment
// says "or nil if none", and the (nil, nil) shape is deliberate: "no active cycle" is not a
// database error. toolGetSprintStatus branched on `err != nil` ONLY and then read `active.ID`,
// so the ordinary no-sprint state took a nil pointer dereference. There is NO panic-recovery
// middleware anywhere in this repo's HTTP stack, so the caller gets a dropped connection rather
// than an answer.
//
// ⚠ THE HANDLER'S OWN COMMENT ASSERTED THE OPPOSITE, IN THE BRANCH THAT CANNOT REACH IT:
// "No active cycle is not an error condition — agents should see a clear 'no active sprint'
// signal instead of a 500." That sentence describes what this file now enforces; before it, the
// branch carrying it was only reachable on a REAL database failure.
//
// ⚠ THE SEAM HAS TWO COPIES AND THE OTHER ONE IS CORRECT — which is why this is a defect and
// not a design question. cycle/handler.go's HTTP GetActive checks `out == nil` and answers 404
// NO_ACTIVE_CYCLE. Same store call, same nil, opposite handling. See TestSeam_BothGetActive-
// ConsumersHandleNil below, which holds the pair together so a future edit cannot silently
// regress the copy that works.
//
// ⚠ WHY W3.4 REACHES IT FIRST: an import creates ISSUES and never a CYCLE. A workspace whose
// content arrived from Jira or Linear therefore has zero cycles, so the first `get_sprint_status`
// an agent makes against a freshly imported workspace is exactly this state.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/cycle"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/testutil"
)

// TestSprintStatus_NoActiveCycle_AnswersInsteadOfPanicking drives the SHIPPED tool through the
// full production middleware chain against real Postgres, for a team that has no active cycle.
//
// RED BEFORE THE FIX: the tool dereferences a nil *model.Cycle. net/http recovers the panic per
// connection and closes it, so this assertion fails on the transport, not on the payload.
func TestSprintStatus_NoActiveCycle_AnswersInsteadOfPanicking(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	seedMember(t, d, ws.ID, "nosprint@corp.com")
	// DELIBERATELY NO CYCLE. This is the state an imported workspace is in.

	// PREMISE, asserted rather than assumed: the store really does answer (nil, nil) here. If a
	// future change made it return an error instead, this test would still pass while measuring
	// a different thing entirely.
	got, err := cycle.NewStore(d.Pool).GetActive(context.Background(), team.ID, ws.ID)
	if err != nil || got != nil {
		t.Fatalf("premise: GetActive with no cycle = (%v, %v), want (nil, nil) — this test is measuring the wrong state", got, err)
	}

	h := mcpChain(d)
	code, resp := callTool(t, h, secret, "nosprint@corp.com", "get_sprint_status",
		map[string]any{"workspace_id": ws.ID, "team_id": team.ID})
	if code != http.StatusOK {
		t.Fatalf("http = %d, want 200 — the tool did not answer at all", code)
	}
	if resp.Error != nil {
		t.Fatalf("get_sprint_status with no active cycle returned a JSON-RPC error %d %q; "+
			"the handler's own comment requires a clear no-sprint signal, not a failure",
			resp.Error.Code, resp.Error.Message)
	}
	payload := toolPayload(t, resp.Result)
	if active, ok := payload["active"].(bool); !ok || active {
		t.Fatalf("payload[active] = %v, want false — a team with no cycle must be reported as having no active sprint; payload=%v", payload["active"], payload)
	}
	if payload["team_id"] != team.ID {
		t.Fatalf("payload[team_id] = %v, want %q", payload["team_id"], team.ID)
	}
	// The no-sprint answer must not carry sprint numbers: a zeroed cycle reads as a real sprint
	// with nothing in it, which is the ambiguity this repo already refused for sample_size.
	for _, k := range []string{"cycle_id", "cycle_name", "total_issues", "completed", "ai_cost_usd"} {
		if v, present := payload[k]; present {
			t.Fatalf("payload carries %q = %v on a workspace with no cycle; a no-sprint answer must not carry sprint figures", k, v)
		}
	}
}

// TestSprintStatus_ActiveCycle_StillAnswers is the MUST-STAY-GREEN companion. Without it, a
// "fix" that made the tool return active:false unconditionally would pass the test above.
func TestSprintStatus_ActiveCycle_StillAnswers(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	seedMember(t, d, ws.ID, "sprint@corp.com")
	cycleID := seedActiveCycle(t, d, ws.ID, team.ID)

	h := mcpChain(d)
	code, resp := callTool(t, h, secret, "sprint@corp.com", "get_sprint_status",
		map[string]any{"workspace_id": ws.ID, "team_id": team.ID})
	if code != http.StatusOK || resp.Error != nil {
		t.Fatalf("http=%d err=%v, want a 200 with no JSON-RPC error", code, resp.Error)
	}
	payload := toolPayload(t, resp.Result)
	if active, ok := payload["active"].(bool); !ok || !active {
		t.Fatalf("payload[active] = %v, want true — a team WITH an active cycle must still report one; payload=%v", payload["active"], payload)
	}
	if payload["cycle_id"] != cycleID {
		t.Fatalf("payload[cycle_id] = %v, want %q", payload["cycle_id"], cycleID)
	}
	// The figures the tool exists to serve must still be present.
	for _, k := range []string{"cycle_name", "start_date", "end_date", "total_issues", "completed", "in_progress", "completion_pct", "ai_cost_usd"} {
		if _, present := payload[k]; !present {
			t.Fatalf("active-sprint payload is missing %q; payload=%v", k, payload)
		}
	}
}

// TestSeam_BothGetActiveConsumersHandleNil holds the OTHER copy of the seam. cycle/handler.go
// answers 404 NO_ACTIVE_CYCLE for the same nil; this asserts it still does, so a future session
// cannot fix one consumer and regress the other. The two copies are what makes the MCP
// behaviour a defect rather than an undecided contract.
func TestSeam_BothGetActiveConsumersHandleNil(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	team := d.Team(t, ws.ID)
	seedMember(t, d, ws.ID, "httptwin@corp.com")

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(gatewayauth.Middleware(secret, func(string) bool { return false }))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), func(string) bool { return false }))
		r.Get("/v1/workspaces/{workspaceID}/teams/{teamID}/cycles/active", cycle.NewHandler(cycle.NewStore(d.Pool)).GetActive)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/"+ws.ID+"/teams/"+team.ID+"/cycles/active", nil)
	req.Header.Set(gatewayauth.HeaderGatewayAuth, secret)
	req.Header.Set(gatewayauth.HeaderUserEmail, "httptwin@corp.com")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("HTTP GetActive with no cycle = %d %s, want 404 — the copy of this seam that "+
			"handles nil correctly has regressed", rr.Code, strings.TrimSpace(rr.Body.String()))
	}
	if !strings.Contains(rr.Body.String(), "NO_ACTIVE_CYCLE") {
		t.Fatalf("HTTP GetActive body = %s, want the NO_ACTIVE_CYCLE code", strings.TrimSpace(rr.Body.String()))
	}
}

// toolPayload unwraps the MCP content envelope into the tool's own JSON object.
func toolPayload(t *testing.T, result json.RawMessage) map[string]any {
	t.Helper()
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &env); err != nil {
		t.Fatalf("decode envelope: %v (result=%s)", err, result)
	}
	if len(env.Content) == 0 {
		t.Fatalf("tool result carried no content: %s", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(env.Content[0].Text), &payload); err != nil {
		t.Fatalf("decode payload: %v (text=%s)", err, env.Content[0].Text)
	}
	return payload
}
