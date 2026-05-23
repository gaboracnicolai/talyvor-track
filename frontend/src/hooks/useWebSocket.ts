import { useEffect, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { RealtimeClient } from "../api/websocket";
import { useUIStore } from "../stores/ui";
import type { RealtimeEvent } from "../api/types";

// Owns one RealtimeClient for the lifetime of the App component. On
// any inbound event that affects issues, invalidate the relevant
// react-query keys so the cached list / detail re-renders with fresh
// data from the next read.
export function useWebSocket(workspaceId: string, memberId: string) {
  const clientRef = useRef<RealtimeClient | null>(null);
  const queryClient = useQueryClient();
  const toast = useUIStore((s) => s.toast);

  useEffect(() => {
    if (!workspaceId || !memberId) return;

    const client = new RealtimeClient(workspaceId, memberId);
    client.connect();
    clientRef.current = client;

    const handler = (ev: RealtimeEvent) => {
      // Coarse invalidation: any issue event refetches the workspace
      // issue list. Finer-grained busting (per-issue, per-team) is a
      // good Phase 9 optimisation but adds little for the common case.
      if (ev.type.startsWith("issue.") || ev.type.startsWith("comment.")) {
        queryClient.invalidateQueries({ queryKey: ["issues"] });
      }
      if (ev.type === "member.joined" || ev.type === "member.left") {
        // Quiet — no toast for presence churn. Future presence-aware
        // UI can subscribe to its own listener for this room.
        return;
      }
    };

    const unsub = client.subscribe(`workspace:${workspaceId}`, handler);

    // 401-style "unauthorized" event from the api client → surface
    // a toast so the user knows to set their API key.
    const onUnauthorized = () => toast("API key not set — open Settings", "warn");
    window.addEventListener("track:unauthorized", onUnauthorized);

    return () => {
      unsub();
      client.close();
      clientRef.current = null;
      window.removeEventListener("track:unauthorized", onUnauthorized);
    };
  }, [workspaceId, memberId, queryClient, toast]);

  return clientRef;
}
