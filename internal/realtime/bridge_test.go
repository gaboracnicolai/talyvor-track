package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newBridgedHub builds a Hub wired to a RedisBridge pointed at mrAddr, models
// one Track instance in a multi-instance deployment. Each hub gets its OWN
// redis client (separate connections), so two of them sharing one miniredis
// model two pods talking through the same Redis.
func newBridgedHub(t *testing.T, mrAddr, originID string, enabled bool) (*Hub, *RedisBridge, context.CancelFunc) {
	t.Helper()
	h := NewHub()
	rc := redis.NewClient(&redis.Options{Addr: mrAddr})
	t.Cleanup(func() { _ = rc.Close() })

	b := NewRedisBridge(rc, h, originID, enabled)
	h.WithBridge(b)

	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	if err := b.Start(ctx); err != nil {
		t.Fatalf("bridge.Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return h, b, cancel
}

// TestBridge_EventOnInstanceAReachesClientOnInstanceB is the headline T13
// guarantee: an event broadcast on instance A is delivered to a client that is
// connected to instance B. Before the bridge this silently dropped — A's
// broadcast only ever reached A's own clients.
func TestBridge_EventOnInstanceAReachesClientOnInstanceB(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cancelA := newBridgedHub(t, mr.Addr(), "instance-A", true)
	defer cancelA()
	hubB, _, cancelB := newBridgedHub(t, mr.Addr(), "instance-B", true)
	defer cancelB()

	// A client connects to instance B and is auto-subscribed to its workspace room.
	bob := newTestClient("b1", "ws-1", "bob")
	hubB.registerForTest(bob)
	time.Sleep(50 * time.Millisecond)
	_ = drain(bob, 1, 200*time.Millisecond) // discard any join noise

	// An event is broadcast on instance A. No client of A is in ws-1, so without
	// the Redis bridge bob would never see it.
	hubA.BroadcastToWorkspace("ws-1", Event{
		Type:        EventIssueCreated,
		WorkspaceID: "ws-1",
		ActorID:     "charlie", // not bob — bob must receive
		Payload:     map[string]string{"id": "i-1"},
	})

	got := drain(bob, 1, 2*time.Second)
	if len(got) != 1 || got[0].Type != EventIssueCreated {
		t.Fatalf("bob on instance B did not receive instance A's event via Redis: %+v", got)
	}
}

// TestBridge_MemberJoinOnAReachesClientOnB locks that hub-generated lifecycle
// events (member.joined, emitted from handleRegister) also cross instances —
// not just Notifier broadcasts.
func TestBridge_MemberJoinOnAReachesClientOnB(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cancelA := newBridgedHub(t, mr.Addr(), "instance-A", true)
	defer cancelA()
	hubB, _, cancelB := newBridgedHub(t, mr.Addr(), "instance-B", true)
	defer cancelB()

	bob := newTestClient("b1", "ws-1", "bob")
	hubB.registerForTest(bob)
	time.Sleep(50 * time.Millisecond)
	_ = drain(bob, 1, 200*time.Millisecond)

	// alice joins on instance A — bob (on B) should see her join.
	alice := newTestClient("a1", "ws-1", "alice")
	hubA.registerForTest(alice)

	got := drain(bob, 1, 2*time.Second)
	if len(got) != 1 || got[0].Type != EventMemberJoined || got[0].ActorID != "alice" {
		t.Fatalf("bob did not see alice's cross-instance join: %+v", got)
	}
}

// TestBridge_NoSelfEcho verifies the origin guard: an instance that publishes
// an event and is also subscribed to the channel must NOT redeliver its own
// event to its local clients (which would double every broadcast).
func TestBridge_NoSelfEcho(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cancel := newBridgedHub(t, mr.Addr(), "instance-A", true)
	defer cancel()

	bob := newTestClient("a2", "ws-1", "bob")
	hubA.registerForTest(bob)
	time.Sleep(50 * time.Millisecond)
	_ = drain(bob, 1, 200*time.Millisecond)

	hubA.BroadcastToWorkspace("ws-1", Event{
		Type:        EventIssueCreated,
		WorkspaceID: "ws-1",
		ActorID:     "charlie",
		Payload:     map[string]string{"id": "i-1"},
	})

	// Exactly one delivery (the local one); the Redis echo of our own publish
	// must be dropped by the origin guard.
	got := drain(bob, 2, 500*time.Millisecond)
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 delivery, got %d — self-echo not suppressed: %+v", len(got), got)
	}
}

// TestBridge_DisabledStaysLocal proves the opt-in contract: with the bridge
// disabled, a single instance behaves exactly as before — nothing crosses Redis.
func TestBridge_DisabledStaysLocal(t *testing.T) {
	mr := miniredis.RunT(t)

	hubA, _, cancelA := newBridgedHub(t, mr.Addr(), "instance-A", false)
	defer cancelA()
	hubB, _, cancelB := newBridgedHub(t, mr.Addr(), "instance-B", false)
	defer cancelB()

	bob := newTestClient("b1", "ws-1", "bob")
	hubB.registerForTest(bob)
	time.Sleep(50 * time.Millisecond)
	_ = drain(bob, 1, 200*time.Millisecond)

	hubA.BroadcastToWorkspace("ws-1", Event{
		Type:        EventIssueCreated,
		WorkspaceID: "ws-1",
		ActorID:     "charlie",
		Payload:     map[string]string{"id": "i-1"},
	})

	got := drain(bob, 1, 500*time.Millisecond)
	if len(got) != 0 {
		t.Fatalf("disabled bridge leaked a cross-instance event: %+v", got)
	}
}
