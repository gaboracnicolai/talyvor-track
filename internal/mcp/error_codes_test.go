package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// TestMCP_ErrorCodesOnTheWireAreTheJSONRPCContract pins the five JSON-RPC error codes
// this server emits to LITERALS, read off the wire.
//
// ⚠ WHY LITERALS, AND WHY THIS FILE EXISTS AT ALL. A defaults census over this repo
// (W3.42: every top-level numeric const in cmd/ + internal/ mutated one at a time,
// judged by CI's exact `go test -timeout 120s -race -count=1 ./...` against real
// Postgres) found THREE of the five codes changeable to any value with the whole
// 42-package suite green: rpcErrMethodNotFnd, rpcErrInternal, and rpcErrUnauthorized.
//
// ⚠⚠ rpcErrUnauthorized is the one that matters, because it LOOKED covered.
// TestMCP_UnmappedTool_DeniedFailClosed asserted
//
//	if resp.Error.Code != rpcErrUnauthorized
//
// — the response compared against the same constant that produced it. Both sides move
// together, so that assertion cannot fail for ANY value of the constant. Its two
// neighbours in the same file assert against the literals -32602 and -32700 and both
// were CAUGHT by the census. The difference was the assertion style, not the coverage.
//
// These codes are a CONTRACT WITH SOMEONE ELSE — an MCP client switches on them — so a
// literal here is not "asserting the source says what the source says": it is the only
// place the wire value is written down independently of the thing that emits it.
func TestMCP_ErrorCodesOnTheWireAreTheJSONRPCContract(t *testing.T) {
	// (1) Parse error — a body that is not JSON at all. Driven through HandleRPC
	// directly because rpcCall marshals valid JSON by construction.
	t.Run("invalid JSON is -32700", func(t *testing.T) {
		s := newTestServer(t)
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{not json"))
		w := httptest.NewRecorder()
		s.HandleRPC(w, req)
		var resp rpcResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("body should be a JSON-RPC envelope: %v; body=%s", err, w.Body.String())
		}
		if resp.Error == nil {
			t.Fatal("a non-JSON body must produce a JSON-RPC error")
		}
		if resp.Error.Code != -32700 {
			t.Errorf("parse error code on the wire = %d, want -32700 (JSON-RPC 2.0 Parse error)", resp.Error.Code)
		}
	})

	// (2) Method not found. This is the code's ONLY reachable emission site:
	// server.go's tools/call `default:` arm is unreachable today because every name
	// that reaches the dispatch switch has already been mapped by toolWorkspace, and
	// an unmapped name is denied at the chokepoint first (see (4)). Recorded rather
	// than implied, so nobody reads a green here as covering both sites.
	t.Run("unknown JSON-RPC method is -32601", func(t *testing.T) {
		s := newTestServer(t)
		resp := rpcCall(t, s, "no/such/method", nil)
		if resp.Error == nil {
			t.Fatal("an unknown method must produce a JSON-RPC error")
		}
		if resp.Error.Code != -32601 {
			t.Errorf("method-not-found code on the wire = %d, want -32601 (JSON-RPC 2.0 Method not found)", resp.Error.Code)
		}
	})

	// (3) Invalid params.
	t.Run("missing required param is -32602", func(t *testing.T) {
		s := newTestServer(t)
		resp := rpcCall(t, s, "tools/call", map[string]any{
			"name": "create_issue",
			"arguments": map[string]any{
				"workspace_id": "ws-1",
				"title":        "missing team",
			},
		})
		if resp.Error == nil {
			t.Fatal("a missing required param must produce a JSON-RPC error")
		}
		if resp.Error.Code != -32602 {
			t.Errorf("invalid-params code on the wire = %d, want -32602 (JSON-RPC 2.0 Invalid params)", resp.Error.Code)
		}
	})

	// (4) The authorization denial — the code the census found unpinned.
	t.Run("an unauthorized tools/call is -32001 and is a legal server-defined code", func(t *testing.T) {
		s := newTestServer(t)
		resp := rpcCall(t, s, "tools/call", map[string]any{
			"name":      "no_such_tool",
			"arguments": map[string]any{},
		})
		if resp.Error == nil {
			t.Fatal("an unmapped tool must be denied with a JSON-RPC error")
		}
		if resp.Error.Code != -32001 {
			t.Errorf("authorization-denial code on the wire = %d, want -32001", resp.Error.Code)
		}
		// The RANGE property, asserted separately from the value: JSON-RPC 2.0 reserves
		// -32768..-32000 for the protocol and carves out -32099..-32000 for
		// implementation-defined server errors. A denial code outside that window either
		// collides with a protocol-defined meaning (a client would read "not authorized"
		// as "internal error" or "invalid request") or leaves the reserved space entirely.
		// This is what the value assertion above is FOR, stated so it is not read as a
		// magic number.
		const serverDefinedLo, serverDefinedHi = -32099, -32000
		if resp.Error.Code < serverDefinedLo || resp.Error.Code > serverDefinedHi {
			t.Errorf("authorization-denial code %d is outside the JSON-RPC server-defined range [%d,%d]",
				resp.Error.Code, serverDefinedLo, serverDefinedHi)
		}
		// …and it must not be one of the four protocol-defined codes this server also
		// emits, or a client cannot tell a denial from a malformed call.
		for _, reserved := range []int{-32700, -32600, -32601, -32602, -32603} {
			if resp.Error.Code == reserved {
				t.Errorf("authorization-denial code collides with protocol-defined %d", reserved)
			}
		}
	})

	// (5) Internal error. Reached by a MAPPED, AUTHORIZED tool whose handler returns a
	// plain error (not an *invalidParamErr) — list_team_members is workspace-keyed, so
	// the chokepoint authorizes ws-1 and dispatch proceeds.
	t.Run("a tool handler error is -32603", func(t *testing.T) {
		s := newServer(
			&fakeIssueStore{},
			&fakeProjectStore{},
			&fakeCycleStore{},
			&fakeAIEngine{available: false},
			&fakeAnalytics{},
			&fakeMembers{listFn: func(ctx context.Context, ws, team string) ([]model.Member, error) {
				return nil, errors.New("boom")
			}},
			"test-version",
		)
		resp := rpcCall(t, s, "tools/call", map[string]any{
			"name": "list_team_members",
			"arguments": map[string]any{
				"workspace_id": "ws-1",
				"team_id":      "team-1",
			},
		})
		if resp.Error == nil {
			t.Fatal("a failing tool handler must produce a JSON-RPC error")
		}
		if resp.Error.Code != -32603 {
			t.Errorf("internal-error code on the wire = %d, want -32603 (JSON-RPC 2.0 Internal error)", resp.Error.Code)
		}
	})
}
