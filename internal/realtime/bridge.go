package realtime

// bridge.go — optional Redis pub/sub fan-out so realtime events cross Track
// instances.
//
// The Hub is process-local: handleBroadcast only ever reaches clients connected
// to THIS instance. Run a second Track instance and an event raised on instance
// A never reaches a client connected to instance B. The RedisBridge closes that
// gap by mirroring every locally-emitted event onto a shared Redis channel and
// re-injecting peers' events into the local delivery path. The flow is:
//
//	local emit  -> Hub.emit         -> deliver locally + RedisBridge.Publish
//	peer publish -> subscribe loop  -> Hub.injectRemote (local delivery ONLY)
//
// injectRemote deliberately does NOT re-publish, and each instance tags its
// publishes with an Origin id and drops the echo of its own — together that
// prevents an event from ping-ponging between instances.
//
// Strictly opt-in (TRACK_HA_ENABLED): when disabled the bridge is a no-op and a
// single instance behaves exactly as it did before the bridge existed. Redis is
// never a NEW hard requirement — it only matters when HA is turned on.

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// bridgeChannel is the single Redis pub/sub channel every instance shares. One
// channel keeps subscription management trivial; each instance still delivers
// only to its own room subscribers (handleBroadcast no-ops for rooms it doesn't
// host), so the extra fan-out is just a cheap JSON decode + map lookup.
const bridgeChannel = "track:realtime"

// publishQueueDepth bounds the in-flight publish buffer. A full queue drops the
// cross-instance copy of an event (logged) rather than stalling the hub — the
// same back-pressure policy the hub already applies to slow clients.
const publishQueueDepth = 256

// redisClient is the subset of *redis.Client the bridge depends on. *redis.Client
// satisfies it unchanged; declaring it here documents exactly which Redis ops the
// bridge uses and gives tests a seam for a fake.
type redisClient interface {
	Publish(ctx context.Context, channel string, message interface{}) *redis.IntCmd
	Subscribe(ctx context.Context, channels ...string) *redis.PubSub
}

// envelope wraps an Event on the wire with the publishing instance's id, so a
// subscriber can drop the echo of its own publishes. Event itself stays
// wire-stable (it is also what clients receive) — the origin lives only here.
type envelope struct {
	Origin string `json:"origin"`
	Event  Event  `json:"event"`
}

// RedisBridge mirrors locally-emitted events onto a shared Redis channel and
// re-injects peers' events into the local hub. The zero value is unusable; build
// one with NewRedisBridge. All methods are safe on a nil receiver and no-op when
// disabled, so callers never have to nil-check.
type RedisBridge struct {
	rdb      redisClient
	hub      *Hub
	channel  string
	originID string
	enabled  bool

	pub chan []byte

	mu  sync.Mutex
	sub *redis.PubSub
}

// NewRedisBridge builds a bridge for hub, identified by originID (unique per
// instance). enabled collapses to false when rdb or hub is nil, so a
// misconfigured HA setup degrades to safe single-instance behaviour rather than
// panicking on the hot path.
func NewRedisBridge(rdb redisClient, hub *Hub, originID string, enabled bool) *RedisBridge {
	return &RedisBridge{
		rdb:      rdb,
		hub:      hub,
		channel:  bridgeChannel,
		originID: originID,
		enabled:  enabled && rdb != nil && hub != nil,
		pub:      make(chan []byte, publishQueueDepth),
	}
}

// Enabled reports whether cross-instance fan-out is active.
func (b *RedisBridge) Enabled() bool { return b != nil && b.enabled }

// Start subscribes to the shared channel and launches the publisher + subscriber
// goroutines, both bound to ctx. No-op when disabled. It blocks until the
// subscription is confirmed so events published immediately after Start are not
// missed (matters for fast startup and tests).
func (b *RedisBridge) Start(ctx context.Context) error {
	if !b.Enabled() {
		return nil
	}
	sub := b.rdb.Subscribe(ctx, b.channel)
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return err
	}
	b.mu.Lock()
	b.sub = sub
	b.mu.Unlock()

	go b.runPublisher(ctx)

	ch := sub.Channel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				b.dispatch(msg.Payload)
			}
		}
	}()
	return nil
}

// runPublisher drains the publish queue onto Redis until ctx is cancelled.
// Publish errors are logged but never terminate the loop — a Redis blip must not
// take down the realtime path (it degrades to local-only delivery).
func (b *RedisBridge) runPublisher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-b.pub:
			if err := b.rdb.Publish(ctx, b.channel, data).Err(); err != nil {
				slog.Warn("realtime: redis publish failed", slog.String("err", err.Error()))
			}
		}
	}
}

// Publish queues ev for fan-out to peer instances. Non-blocking: a full queue
// drops the cross-instance copy with a warning. No-op when disabled.
func (b *RedisBridge) Publish(ev Event) {
	if !b.Enabled() {
		return
	}
	data, err := json.Marshal(envelope{Origin: b.originID, Event: ev})
	if err != nil {
		slog.Warn("realtime: marshal bridge envelope", slog.String("err", err.Error()))
		return
	}
	select {
	case b.pub <- data:
	default:
		slog.Warn("realtime: redis publish queue full; dropping cross-instance event",
			slog.String("event", string(ev.Type)))
	}
}

// dispatch decodes one envelope from a peer and injects the event locally,
// dropping the echo of our own publishes.
func (b *RedisBridge) dispatch(payload string) {
	var env envelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return
	}
	if env.Origin == b.originID {
		return // our own publish — already delivered locally
	}
	b.hub.injectRemote(env.Event)
}

// Close tears down the subscription. Safe to call when disabled or never
// started; the goroutines themselves exit when their context is cancelled.
func (b *RedisBridge) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sub != nil {
		err := b.sub.Close()
		b.sub = nil
		return err
	}
	return nil
}
