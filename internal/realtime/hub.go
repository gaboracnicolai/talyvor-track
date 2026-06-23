// Package realtime implements the WebSocket fan-out for live issue,
// comment, and presence updates.
//
// One Hub owns every active connection. Clients register on connect,
// subscribe to per-workspace / per-team / per-issue "rooms", and
// receive Events via a buffered send channel. The hub's central event
// loop is the only goroutine that mutates the clients map — everything
// else flows through register / unregister / broadcast channels, so
// concurrent access is structurally safe.
package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// EventType enumerates the wire-level event names. Clients filter on
// these to decide whether to update local state.
type EventType string

const (
	EventIssueCreated   EventType = "issue.created"
	EventIssueUpdated   EventType = "issue.updated"
	EventIssueDeleted   EventType = "issue.deleted"
	EventCommentCreated EventType = "comment.created"
	EventCommentUpdated EventType = "comment.updated"
	EventCommentDeleted EventType = "comment.deleted"
	EventCycleUpdated   EventType = "cycle.updated"
	EventMemberJoined   EventType = "member.joined"
	EventMemberLeft     EventType = "member.left"
	EventPresence       EventType = "presence"
)

// Event is the on-the-wire shape every broadcast uses. Payload is
// type-erased so the hub doesn't have to know about model.Issue,
// model.Comment, etc. — callers fill it in with whatever JSON-
// serialisable struct fits the event.
type Event struct {
	Type        EventType `json:"type"`
	WorkspaceID string    `json:"workspace_id"`
	RoomID      string    `json:"room_id"`
	ActorID     string    `json:"actor_id"`
	Payload     any       `json:"payload"`
	Timestamp   time.Time `json:"timestamp"`
}

// Client is one connected member. The conn field is nil in tests that
// exercise the hub directly without a real WebSocket; the send channel
// is the universal interface.
type Client struct {
	ID          string
	WorkspaceID string
	MemberID    string
	conn        *websocket.Conn
	send        chan []byte
	rooms       map[string]struct{}
}

const (
	clientSendBuffer = 256
	writeTimeout     = 10 * time.Second
	readTimeout      = 60 * time.Second
	pingInterval     = 30 * time.Second
)

func newClient(id, workspaceID, memberID string, conn *websocket.Conn) *Client {
	return &Client{
		ID:          id,
		WorkspaceID: workspaceID,
		MemberID:    memberID,
		conn:        conn,
		send:        make(chan []byte, clientSendBuffer),
		rooms:       make(map[string]struct{}),
	}
}

// Hub owns every connected client + the room → clients mapping. All
// mutations flow through channels so the central event loop is the
// only goroutine touching the maps. That removes lock contention from
// the hot broadcast path.
type Hub struct {
	mu         sync.RWMutex
	clients    map[string]*Client            // clientID → client
	rooms      map[string]map[string]*Client // roomID → clientID → client

	register   chan *Client
	unregister chan *Client
	broadcast  chan Event

	// bridge, when set, mirrors locally-emitted events to peer instances over
	// Redis and re-injects theirs (see bridge.go). nil in single-instance mode —
	// every bridge method is nil-safe, so the hot path stays branch-light.
	bridge *RedisBridge
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]*Client),
		rooms:      make(map[string]map[string]*Client),
		register:   make(chan *Client, 16),
		unregister: make(chan *Client, 16),
		broadcast:  make(chan Event, 128),
	}
}

// WithBridge attaches a Redis bridge so events fan out across instances. Call it
// during setup, before Run — the field is not mutated once the event loop is
// running. Returns the hub for chaining, matching Track's store/handler builders.
func (h *Hub) WithBridge(b *RedisBridge) *Hub {
	h.bridge = b
	return h
}

// Run is the central event loop. Exits when ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.handleRegister(c)
		case c := <-h.unregister:
			h.handleUnregister(c)
		case ev := <-h.broadcast:
			h.handleBroadcast(ev)
		}
	}
}

func (h *Hub) handleRegister(c *Client) {
	h.mu.Lock()
	h.clients[c.ID] = c
	// Every client is auto-subscribed to its workspace room so it
	// receives top-level workspace events without an explicit
	// subscribe message.
	wsRoom := "workspace:" + c.WorkspaceID
	c.rooms[wsRoom] = struct{}{}
	if _, ok := h.rooms[wsRoom]; !ok {
		h.rooms[wsRoom] = make(map[string]*Client)
	}
	h.rooms[wsRoom][c.ID] = c
	h.mu.Unlock()

	// Notify everyone else in the workspace that this member arrived.
	h.emit(Event{
		Type:        EventMemberJoined,
		WorkspaceID: c.WorkspaceID,
		RoomID:      wsRoom,
		ActorID:     c.MemberID,
		Payload:     map[string]string{"member_id": c.MemberID},
		Timestamp:   time.Now().UTC(),
	})
}

func (h *Hub) handleUnregister(c *Client) {
	h.mu.Lock()
	if _, ok := h.clients[c.ID]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.clients, c.ID)
	for roomID := range c.rooms {
		if room, ok := h.rooms[roomID]; ok {
			delete(room, c.ID)
			if len(room) == 0 {
				delete(h.rooms, roomID)
			}
		}
	}
	wsRoom := "workspace:" + c.WorkspaceID
	close(c.send)
	h.mu.Unlock()

	h.emit(Event{
		Type:        EventMemberLeft,
		WorkspaceID: c.WorkspaceID,
		RoomID:      wsRoom,
		ActorID:     c.MemberID,
		Payload:     map[string]string{"member_id": c.MemberID},
		Timestamp:   time.Now().UTC(),
	})
}

func (h *Hub) handleBroadcast(ev Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("realtime: marshal event", slog.String("err", err.Error()))
		return
	}
	h.mu.RLock()
	room, ok := h.rooms[ev.RoomID]
	if !ok {
		h.mu.RUnlock()
		return
	}
	// Snapshot the recipient list under the lock, then dispatch
	// outside it so a slow client can't block other deliveries.
	recipients := make([]*Client, 0, len(room))
	for _, c := range room {
		if c.MemberID == ev.ActorID {
			// Skip the actor — no echo.
			continue
		}
		recipients = append(recipients, c)
	}
	h.mu.RUnlock()

	for _, c := range recipients {
		// Non-blocking: drop the message if the client's send buffer
		// is full. Slow clients lose messages, not the whole hub.
		select {
		case c.send <- data:
		default:
			slog.Warn("realtime: dropped message",
				slog.String("client_id", c.ID),
				slog.String("event", string(ev.Type)),
			)
		}
	}
}

// BroadcastToRoom publishes an event to a specific room. Non-blocking
// — if the hub's broadcast channel is full the event is dropped and a
// warning is logged.
func (h *Hub) BroadcastToRoom(roomID string, ev Event) {
	ev.RoomID = roomID
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	h.emit(ev)
}

// emit queues ev for delivery to this hub's local clients and, when a Redis
// bridge is attached, publishes it so peer instances deliver it to THEIR local
// clients too. Both paths are non-blocking — a full buffer drops the event with
// a warning rather than stalling the caller (and, since emit also runs on the
// event loop for member join/leave, a non-blocking send avoids the loop ever
// blocking on the channel it itself drains).
func (h *Hub) emit(ev Event) {
	select {
	case h.broadcast <- ev:
	default:
		slog.Warn("realtime: broadcast channel full; dropping event",
			slog.String("room", ev.RoomID),
			slog.String("event", string(ev.Type)),
		)
	}
	h.bridge.Publish(ev) // nil-safe; no-op in single-instance mode
}

// injectRemote queues an event received from a peer instance (via the Redis
// bridge) for delivery to THIS hub's local clients. It deliberately does NOT
// re-publish — that, plus the bridge's origin guard, is what stops an event
// from ping-ponging between instances.
func (h *Hub) injectRemote(ev Event) {
	select {
	case h.broadcast <- ev:
	default:
		slog.Warn("realtime: broadcast channel full; dropping remote event",
			slog.String("room", ev.RoomID),
			slog.String("event", string(ev.Type)),
		)
	}
}

// BroadcastToWorkspace is a convenience wrapper for the common
// "everyone in this workspace" case.
func (h *Hub) BroadcastToWorkspace(wsID string, ev Event) {
	h.BroadcastToRoom("workspace:"+wsID, ev)
}

// Subscribe adds a client to a room. Thread-safe; can be called from
// the WebSocket read pump after parsing a subscribe message.
func (h *Hub) Subscribe(clientID, roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.clients[clientID]
	if !ok {
		return
	}
	c.rooms[roomID] = struct{}{}
	if _, ok := h.rooms[roomID]; !ok {
		h.rooms[roomID] = make(map[string]*Client)
	}
	h.rooms[roomID][clientID] = c
}

func (h *Hub) Unsubscribe(clientID, roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.clients[clientID]
	if ok {
		delete(c.rooms, roomID)
	}
	if room, ok := h.rooms[roomID]; ok {
		delete(room, clientID)
		if len(room) == 0 {
			delete(h.rooms, roomID)
		}
	}
}

func (h *Hub) ClientCount(roomID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms[roomID])
}

// upgrader is the gorilla/websocket upgrader. Origin checks are
// permissive in Phase 3 — Phase 4 can restrict to the configured
// frontend origin.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

// ServeWS upgrades the request to a WebSocket and spawns the
// read+write pumps for that client. Returns immediately; lifecycle is
// owned by the spawned goroutines + the hub's event loop.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	workspaceID := q.Get("workspace_id")
	memberID := q.Get("member_id")
	if workspaceID == "" || memberID == "" {
		http.Error(w, "workspace_id and member_id query parameters required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("realtime: ws upgrade failed", slog.String("err", err.Error()))
		return
	}
	client := newClient(uuid.NewString(), workspaceID, memberID, conn)
	h.register <- client

	go h.readPump(client)
	go h.writePump(client)
}

// readPump consumes messages from the WebSocket. The only client →
// server messages are subscribe / unsubscribe / ping. Anything else
// is logged and ignored.
func (h *Hub) readPump(c *Client) {
	defer func() {
		h.unregister <- c
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(64 * 1024)
	_ = c.conn.SetReadDeadline(time.Now().Add(readTimeout))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(readTimeout))
	})

	type clientMsg struct {
		Type   string `json:"type"`
		RoomID string `json:"room_id,omitempty"`
	}
	for {
		var msg clientMsg
		if err := c.conn.ReadJSON(&msg); err != nil {
			// Most disconnects flow through here — abnormal closure,
			// timeout, client navigated away. Log at debug.
			return
		}
		switch msg.Type {
		case "subscribe":
			if msg.RoomID != "" {
				h.Subscribe(c.ID, msg.RoomID)
			}
		case "unsubscribe":
			if msg.RoomID != "" {
				h.Unsubscribe(c.ID, msg.RoomID)
			}
		case "ping":
			// Application-level ping — respond on the send channel so
			// the response goes through the normal write pump.
			pong, _ := json.Marshal(map[string]string{"type": "pong"})
			select {
			case c.send <- pong:
			default:
			}
		}
	}
}

// writePump drains the send channel onto the WebSocket. A ticker
// emits protocol-level pings every 30s so middleboxes don't drop the
// connection during a quiet period.
func (h *Hub) writePump(c *Client) {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if !ok {
				// Hub closed the send channel — graceful shutdown.
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// registerForTest exposes the channel-based register flow so tests
// can drive the hub without running ServeWS / a real WebSocket. The
// hub still observes the same lifecycle: client appears in the map,
// auto-subscribes to its workspace room.
func (h *Hub) registerForTest(c *Client) { h.register <- c }

// unregisterForTest is the test companion to registerForTest.
func (h *Hub) unregisterForTest(c *Client) { h.unregister <- c }
