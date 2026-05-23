// WebSocket client with exponential-backoff reconnect. Exposes a
// minimal Subscription API: subscribe to a room, get callbacks for
// events targeted at that room. The hook layer (useWebSocket) drives
// the connection lifecycle.

import type { RealtimeEvent } from "./types";

type Listener = (ev: RealtimeEvent) => void;

const BASE_WS =
  (import.meta.env.VITE_API_URL ?? "").replace(/^http/, "ws") || "ws://localhost:3000";

export class RealtimeClient {
  private ws: WebSocket | null = null;
  private listeners = new Map<string, Set<Listener>>(); // room → listeners
  private reconnectAttempts = 0;
  private closed = false;
  private rooms = new Set<string>(); // rooms to re-subscribe on reconnect
  private workspaceId: string;
  private memberId: string;

  constructor(workspaceId: string, memberId: string) {
    this.workspaceId = workspaceId;
    this.memberId = memberId;
  }

  connect(): void {
    if (!this.workspaceId || !this.memberId) return;
    if (this.closed) return;
    const url = `${BASE_WS}/v1/ws?workspace_id=${this.workspaceId}&member_id=${this.memberId}`;
    this.ws = new WebSocket(url);

    this.ws.addEventListener("open", () => {
      this.reconnectAttempts = 0;
      // Re-subscribe to every room we were listening to before the
      // reconnect so listeners get events without manual re-attach.
      for (const room of this.rooms) {
        this.send({ type: "subscribe", room_id: room });
      }
    });

    this.ws.addEventListener("message", (ev) => {
      try {
        const parsed = JSON.parse(ev.data as string) as RealtimeEvent;
        const set = this.listeners.get(parsed.room_id);
        if (set) {
          for (const l of set) l(parsed);
        }
      } catch {
        // Ignore non-JSON frames (server should only send JSON).
      }
    });

    this.ws.addEventListener("close", () => {
      if (this.closed) return;
      // Exponential backoff: 1s, 2s, 4s, ..., max 30s.
      const delay = Math.min(30_000, 1000 * 2 ** this.reconnectAttempts);
      this.reconnectAttempts++;
      setTimeout(() => this.connect(), delay);
    });

    this.ws.addEventListener("error", () => {
      // Errors trigger close; close handler does reconnect.
    });
  }

  subscribe(room: string, listener: Listener): () => void {
    if (!this.listeners.has(room)) this.listeners.set(room, new Set());
    this.listeners.get(room)!.add(listener);
    this.rooms.add(room);
    this.send({ type: "subscribe", room_id: room });
    return () => {
      const set = this.listeners.get(room);
      if (!set) return;
      set.delete(listener);
      if (set.size === 0) {
        this.listeners.delete(room);
        this.rooms.delete(room);
        this.send({ type: "unsubscribe", room_id: room });
      }
    };
  }

  private send(msg: object): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg));
    }
  }

  close(): void {
    this.closed = true;
    this.ws?.close();
    this.ws = null;
    this.listeners.clear();
    this.rooms.clear();
  }
}
