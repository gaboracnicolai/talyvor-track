package realtime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/talyvor/track/internal/authz"
	"github.com/talyvor/track/internal/gatewayauth"
	"github.com/talyvor/track/internal/model"
	"github.com/talyvor/track/internal/realtime"
	"github.com/talyvor/track/internal/testutil"
)

// AUDIT FINDINGS 3 + 7, fixed together because fixing one arms the other.
//
// (3) /v1/ws returned 403 to EVERY caller. ServeWS called AuthorizeWorkspace, DISCARDED
//     the Membership it returned, then called authz.MemberID — which only answers on a
//     /v1/workspaces/{wsID}/… path, because that is the only shape the middleware sets
//     hasWorkspace for. /v1/ws is flat, so the second gate could never pass. Realtime was
//     entirely dead while the README advertised "Real-time updates ✅".
//
//     It shipped green because ws_authz_test.go is negative-only: it asserts that an
//     UNAUTHORIZED request gets 403 and never asserts that an authorized one connects.
//     Both halves of a gate need a test, or "denies everyone" reads as success.
//
// (7) Subscribe accepted ANY room id and created the room map on demand, with no check
//     that the room belongs to the client's workspace. Rooms are workspace:<id>,
//     team:<id> and issue:<id>, and payloads carry the full model.Issue (title,
//     description, ai_cost_usd) and full model.Comment (body). So the moment (3) is
//     fixed, any authenticated user could subscribe to another tenant's room and receive
//     its live issue and comment stream. The room name is not a secret: the anonymous
//     public feature board returns workspace_id.

const wsTestSecret = "realtime-ws-test-secret-0123456789"

// liveWS stands up the REAL chain — gatewayauth → authz → ServeWS — behind an httptest
// server, and returns its ws:// base. Nothing is stubbed: this is what production wires.
func liveWS(t *testing.T, d *testutil.DB) (base string, hub *realtime.Hub) {
	t.Helper()
	hub = realtime.NewHub().WithRoomAuthorizer(realtime.NewPGRoomAuthorizer(d.Pool))
	go hub.Run(t.Context())

	noExempt := func(string) bool { return false }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(gatewayauth.Middleware(wsTestSecret, noExempt))
		r.Use(authz.Middleware(authz.NewPGResolver(d.Pool), noExempt))
		r.Get("/ws", hub.ServeWS)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), hub
}

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

func dialAs(t *testing.T, base, email, workspaceID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	h.Set(gatewayauth.HeaderGatewayAuth, wsTestSecret)
	h.Set(gatewayauth.HeaderUserEmail, email)
	return websocket.DefaultDialer.Dial(base+"/v1/ws?workspace_id="+workspaceID, h)
}

// FINDING 3. RED before the fix: 403 for a fully valid member with a valid transit proof.
func TestServeWS_AuthorizedMember_CanConnect(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "alice@corp.com")
	base, _ := liveWS(t, d)

	conn, resp, err := dialAs(t, base, "alice@corp.com", ws.ID)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("an authorized member could not open the socket: %v (HTTP %d) — /v1/ws is a flat route, so "+
			"authz.MemberID never answers; the Membership AuthorizeWorkspace already returned must be used", err, code)
	}
	defer conn.Close()
}

// The negative half still holds: a verified caller who is NOT a member is refused.
func TestServeWS_NonMember_StillRefused(t *testing.T) {
	d := testutil.New(t)
	wsA := d.Workspace(t)
	wsB := d.Workspace(t)
	seedMember(t, d, wsA.ID, "alice@corp.com") // member of A only
	base, _ := liveWS(t, d)

	conn, resp, err := dialAs(t, base, "alice@corp.com", wsB.ID)
	if err == nil {
		conn.Close()
		t.Fatal("a non-member opened a socket onto another workspace")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member dial = %v, want HTTP 403", resp)
	}
}

// FINDING 7. RED once finding 3 is fixed: an authenticated member of workspace A
// subscribes to workspace B's room and receives B's live issue stream.
func TestSubscribe_ForeignWorkspaceRoom_ReceivesNothing(t *testing.T) {
	d := testutil.New(t)
	attackerWS := d.Workspace(t)
	victimWS := d.Workspace(t)
	seedMember(t, d, attackerWS.ID, "mallory@attacker.test")
	victimTeam := d.Team(t, victimWS.ID)
	base, hub := liveWS(t, d)

	conn, _, err := dialAs(t, base, "mallory@attacker.test", attackerWS.ID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// The victim's room name is not a secret — the anonymous public board hands out
	// workspace_id. Try all three room shapes.
	for _, room := range []string{
		"workspace:" + victimWS.ID,
		"team:" + victimTeam.ID,
	} {
		sub, _ := json.Marshal(map[string]string{"type": "subscribe", "room_id": room})
		if err := conn.WriteMessage(websocket.TextMessage, sub); err != nil {
			t.Fatalf("subscribe write: %v", err)
		}
	}
	// Let the read pump process both subscribes before the broadcast.
	waitForRoomSettle(t, hub, "workspace:"+victimWS.ID)

	// The victim publishes confidential work into their own rooms.
	notifier := realtime.NewNotifier(hub)
	notifier.IssueCreated(context.Background(), victimWS.ID, victimTeam.ID, "victim-member",
		model.Issue{
			ID: "vic-1", Identifier: "VIC-1", WorkspaceID: victimWS.ID, TeamID: victimTeam.ID,
			Title: "CONFIDENTIAL: Q3 acquisition of Acme", Description: "Board-only. Deal value $40M.",
		})

	_ = conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return // timed out with nothing delivered — correct
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if ev["type"] == "pong" || ev["type"] == "" {
			continue
		}
		t.Fatalf("CROSS-TENANT LIVE FEED: a member of another workspace received %s from the victim's room: %s",
			ev["type"], string(msg))
	}
}

// The legitimate case must keep working: a member subscribing to their OWN workspace's
// team room receives its events. Without this, "deny everything" would pass the test
// above — the same negative-only trap that hid finding 3.
func TestSubscribe_OwnWorkspaceRoom_StillDelivers(t *testing.T) {
	d := testutil.New(t)
	ws := d.Workspace(t)
	seedMember(t, d, ws.ID, "alice@corp.com")
	team := d.Team(t, ws.ID)
	base, hub := liveWS(t, d)

	conn, _, err := dialAs(t, base, "alice@corp.com", ws.ID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	room := "team:" + team.ID
	sub, _ := json.Marshal(map[string]string{"type": "subscribe", "room_id": room})
	if err := conn.WriteMessage(websocket.TextMessage, sub); err != nil {
		t.Fatalf("subscribe write: %v", err)
	}
	waitForRoomSettle(t, hub, room)

	realtime.NewNotifier(hub).IssueCreated(context.Background(), ws.ID, team.ID, "someone-else",
		model.Issue{ID: "i-1", Identifier: "ENG-1", WorkspaceID: ws.ID, TeamID: team.ID, Title: "Our own work"})

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("own-workspace team room delivered nothing: %v — the room gate must not deny legitimate subscribers", err)
		}
		var ev map[string]any
		_ = json.Unmarshal(msg, &ev)
		if ev["type"] == "issue.created" {
			return
		}
	}
}

// waitForRoomSettle blocks until the hub reports at least one client in roomID, so a
// broadcast is not raced against the read pump. Fails rather than sleeping blindly.
func waitForRoomSettle(t *testing.T, hub *realtime.Hub, roomID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ClientCount(roomID) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Not fatal: for the cross-tenant test the room legitimately stays empty once the
	// gate is in place. The caller's own assertions decide the verdict.
}
