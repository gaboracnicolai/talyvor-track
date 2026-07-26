package realtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// newTestClient builds a Client without a real WebSocket conn. The
// hub never touches the conn field outside the read/write pumps, so
// tests can drive the hub end-to-end through the channel API.
func newTestClient(id, workspaceID, memberID string) *Client {
	return &Client{
		ID:          id,
		WorkspaceID: workspaceID,
		MemberID:    memberID,
		send:        make(chan []byte, clientSendBuffer),
		rooms:       make(map[string]struct{}),
	}
}

// runHub starts the hub's event loop and returns a cancel function
// the test should defer to shut it down cleanly.
func runHub(t *testing.T) (*Hub, context.CancelFunc) {
	t.Helper()
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	return h, cancel
}

// drain reads up to maxMessages from c.send within timeout. The hub's
// register flow auto-broadcasts a member.joined event, so tests need
// to discard those before asserting on the events they care about.
func drain(c *Client, count int, timeout time.Duration) []Event {
	var out []Event
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for len(out) < count {
		select {
		case msg := <-c.send:
			var ev Event
			if err := json.Unmarshal(msg, &ev); err == nil {
				out = append(out, ev)
			}
		case <-deadline.C:
			return out
		}
	}
	return out
}

func TestHub_RegistersAndUnregistersClients(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()

	c := newTestClient("c1", "ws-1", "alice")
	h.registerForTest(c)

	// Give the loop a tick to process.
	time.Sleep(20 * time.Millisecond)
	if h.ClientCount("workspace:ws-1") != 1 {
		t.Errorf("ClientCount = %d, want 1", h.ClientCount("workspace:ws-1"))
	}

	h.unregisterForTest(c)
	time.Sleep(20 * time.Millisecond)
	if h.ClientCount("workspace:ws-1") != 0 {
		t.Errorf("ClientCount after unregister = %d, want 0", h.ClientCount("workspace:ws-1"))
	}
}

func TestBroadcastToRoom_DeliversToSubscribedClients(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()

	alice := newTestClient("c1", "ws-1", "alice")
	bob := newTestClient("c2", "ws-1", "bob")
	h.registerForTest(alice)
	h.registerForTest(bob)
	time.Sleep(20 * time.Millisecond)
	// Drain the auto-fired member.joined events.
	_ = drain(alice, 1, 100*time.Millisecond) // saw bob join
	_ = drain(bob, 1, 100*time.Millisecond)   // saw alice join (or bob's own)

	h.BroadcastToRoom("workspace:ws-1", Event{
		Type:        EventIssueCreated,
		WorkspaceID: "ws-1",
		ActorID:     "charlie", // neither alice nor bob — both receive
		Payload:     map[string]string{"id": "i-1"},
	})

	gotAlice := drain(alice, 1, 100*time.Millisecond)
	gotBob := drain(bob, 1, 100*time.Millisecond)

	if len(gotAlice) != 1 || gotAlice[0].Type != EventIssueCreated {
		t.Errorf("alice did not receive event: %+v", gotAlice)
	}
	if len(gotBob) != 1 || gotBob[0].Type != EventIssueCreated {
		t.Errorf("bob did not receive event: %+v", gotBob)
	}
}

func TestBroadcastToRoom_SkipsActor(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()

	alice := newTestClient("c1", "ws-1", "alice")
	bob := newTestClient("c2", "ws-1", "bob")
	h.registerForTest(alice)
	h.registerForTest(bob)
	time.Sleep(20 * time.Millisecond)
	_ = drain(alice, 1, 100*time.Millisecond)
	_ = drain(bob, 1, 100*time.Millisecond)

	h.BroadcastToRoom("workspace:ws-1", Event{
		Type:        EventIssueCreated,
		WorkspaceID: "ws-1",
		ActorID:     "alice", // her own action — she should NOT receive
		Payload:     map[string]string{},
	})

	gotAlice := drain(alice, 1, 100*time.Millisecond)
	gotBob := drain(bob, 1, 100*time.Millisecond)

	if len(gotAlice) != 0 {
		t.Errorf("actor should not receive own event; got %+v", gotAlice)
	}
	if len(gotBob) != 1 {
		t.Errorf("non-actor bob should receive event; got %+v", gotBob)
	}
}

// Subscribe is now a tenancy chokepoint (audit finding 7): a room is joined only when it
// belongs to the client's authorized workspace. This test previously subscribed a ws-1
// client to "team:eng" with no ownership check at all and asserted success — it was
// pinning the hole. The three branches are covered separately below.
func TestSubscribe_OwnWorkspaceRoom_NeedsNoAuthorizer(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()

	c := newTestClient("c1", "ws-1", "alice")
	h.registerForTest(c)
	time.Sleep(20 * time.Millisecond)

	room := "workspace:ws-1"
	if !h.Subscribe("c1", room) {
		t.Fatal("a client was refused its OWN workspace room — that decision is a string compare, no lookup needed")
	}
	if h.ClientCount(room) != 1 {
		t.Errorf("ClientCount(%s) = %d, want 1", room, h.ClientCount(room))
	}
}

// A foreign workspace room is refused on the string compare alone.
func TestSubscribe_ForeignWorkspaceRoom_Refused(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()

	h.registerForTest(newTestClient("c1", "ws-1", "alice"))
	time.Sleep(20 * time.Millisecond)

	if h.Subscribe("c1", "workspace:ws-2") {
		t.Fatal("a ws-1 client joined ws-2's room")
	}
	if h.ClientCount("workspace:ws-2") != 0 {
		t.Errorf("ClientCount(workspace:ws-2) = %d, want 0", h.ClientCount("workspace:ws-2"))
	}
}

// No authorizer wired ⇒ object-keyed rooms are DENIED, not allowed. A hub that cannot
// prove ownership must refuse; degrading the feature is correct, opening it is not.
func TestSubscribe_ObjectRoom_WithoutAuthorizer_Refused(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()

	h.registerForTest(newTestClient("c1", "ws-1", "alice"))
	time.Sleep(20 * time.Millisecond)

	for _, room := range []string{"team:eng", "issue:i-1"} {
		if h.Subscribe("c1", room) {
			t.Fatalf("joined %s with no room authorizer — ownership was never proven", room)
		}
	}
}

// With an authorizer, ownership decides — and a room in another workspace still loses.
func TestSubscribe_ObjectRoom_FollowsAuthorizer(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()
	h.WithRoomAuthorizer(fakeRoomAuth{owned: map[string]string{"eng": "ws-1", "other": "ws-2"}})

	h.registerForTest(newTestClient("c1", "ws-1", "alice"))
	time.Sleep(20 * time.Millisecond)

	if !h.Subscribe("c1", "team:eng") {
		t.Fatal("refused a team room the authorizer says is in ws-1")
	}
	if h.Subscribe("c1", "team:other") {
		t.Fatal("joined a team room the authorizer says is in ws-2")
	}
	if h.Subscribe("c1", "team:unknown") {
		t.Fatal("joined a team room the authorizer does not recognise — unknown must deny")
	}
}

// Malformed and unknown-kind room ids are refused rather than guessed at.
func TestSubscribe_MalformedRoomID_Refused(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()
	h.WithRoomAuthorizer(fakeRoomAuth{owned: map[string]string{"eng": "ws-1"}})

	h.registerForTest(newTestClient("c1", "ws-1", "alice"))
	time.Sleep(20 * time.Millisecond)

	for _, room := range []string{"", "workspace:", ":ws-1", "ws-1", "presence:ws-1", "team:a:b"} {
		if h.Subscribe("c1", room) {
			t.Errorf("joined malformed/unknown room %q", room)
		}
	}
}

// fakeRoomAuth is a decision table: objectID → owning workspace.
type fakeRoomAuth struct{ owned map[string]string }

func (f fakeRoomAuth) AuthorizeRoom(_ context.Context, workspaceID, _, objectID string) (bool, error) {
	ws, ok := f.owned[objectID]
	return ok && ws == workspaceID, nil
}

func TestUnsubscribe_RemovesClientFromRoom(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()
	h.WithRoomAuthorizer(fakeRoomAuth{owned: map[string]string{"eng": "ws-1"}})

	c := newTestClient("c1", "ws-1", "alice")
	h.registerForTest(c)
	time.Sleep(20 * time.Millisecond)
	h.Subscribe("c1", "team:eng")
	h.Unsubscribe("c1", "team:eng")
	if h.ClientCount("team:eng") != 0 {
		t.Errorf("ClientCount(team:eng) = %d, want 0 after unsubscribe", h.ClientCount("team:eng"))
	}
}

func TestClientCount_ReturnsCorrectNumber(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()

	for i, m := range []string{"a", "b", "c"} {
		c := newTestClient(string('A'+rune(i)), "ws-1", m)
		h.registerForTest(c)
	}
	time.Sleep(40 * time.Millisecond)
	if got := h.ClientCount("workspace:ws-1"); got != 3 {
		t.Errorf("ClientCount = %d, want 3", got)
	}
}

func TestEventMemberJoined_FiredOnConnect(t *testing.T) {
	h, cancel := runHub(t)
	defer cancel()

	alice := newTestClient("c1", "ws-1", "alice")
	h.registerForTest(alice)
	time.Sleep(20 * time.Millisecond)

	bob := newTestClient("c2", "ws-1", "bob")
	h.registerForTest(bob)

	// Alice should receive bob's join event (actor=bob, recipient=alice).
	got := drain(alice, 1, 200*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected alice to see bob's join; got %d events", len(got))
	}
	if got[0].Type != EventMemberJoined {
		t.Errorf("event type = %q, want member.joined", got[0].Type)
	}
	if got[0].ActorID != "bob" {
		t.Errorf("actor = %q, want bob", got[0].ActorID)
	}
}

func TestPresence_AddAndGetViewers(t *testing.T) {
	p := NewPresenceStore()
	p.AddViewer("issue-1", Viewer{MemberID: "alice", Name: "Alice"})
	p.AddViewer("issue-1", Viewer{MemberID: "bob", Name: "Bob"})

	got := p.GetViewers("issue-1")
	if len(got) != 2 {
		t.Fatalf("got %d viewers, want 2", len(got))
	}

	p.RemoveViewer("issue-1", "alice")
	got = p.GetViewers("issue-1")
	if len(got) != 1 || got[0].MemberID != "bob" {
		t.Errorf("after remove: got %+v, want only bob", got)
	}
}

func TestPresence_ExpiresAfterTTL(t *testing.T) {
	p := NewPresenceStore()
	old := time.Now().UTC().Add(-2 * presenceTTL)
	p.AddViewer("issue-1", Viewer{MemberID: "stale", Name: "Stale", Since: old})
	p.AddViewer("issue-1", Viewer{MemberID: "fresh", Name: "Fresh"})

	got := p.GetViewers("issue-1")
	if len(got) != 1 {
		t.Fatalf("got %d, want 1 (stale viewer should be pruned)", len(got))
	}
	if got[0].MemberID != "fresh" {
		t.Errorf("expected fresh; got %+v", got)
	}
}
