import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { issuesApi, type BulkUpdateItem, type ListIssuesParams } from "../api/issues";
import type { Issue } from "../api/types";
import { useWorkspace } from "./useWorkspace";
import { useUIStore } from "../stores/ui";

// Centralised issue-data hooks. Components use these so the
// cache-key naming convention stays consistent across the app.
export function useIssues(params: ListIssuesParams = {}) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["issues", workspaceId, params],
    queryFn: () => issuesApi.list(workspaceId, params),
    enabled: !!workspaceId,
  });
}

export function useIssue(id: string | null) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["issue", workspaceId, id],
    queryFn: () => issuesApi.get(workspaceId, id!),
    enabled: !!id,
  });
}

export function useUpdateIssue() {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (vars: { id: string; updates: Record<string, unknown> }) =>
      issuesApi.update(workspaceId, vars.id, vars.updates),
    onSuccess: (updated: Issue) => {
      qc.invalidateQueries({ queryKey: ["issues"] });
      qc.setQueryData(["issue", workspaceId, updated.id], updated);
    },
    onError: (err) => toast(err.message, "error"),
  });
}

export function useCreateIssue() {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (body: Partial<Issue>) => issuesApi.create(workspaceId, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["issues"] }),
    onError: (err) => toast(err.message, "error"),
  });
}

// useBulkUpdateIssues is the kanban-drag mutation. It patches every
// affected issue in one request and applies the change to the local
// react-query cache before the network round-trip so the card lands
// in its new column instantly.
//
// On failure we restore the previous cache snapshot — the UI flips
// back to the pre-drop state and a toast tells the user why. On
// success we let the server-truth refetch via invalidate, so any
// secondary fields the backend computes (updated_at, completed_at)
// re-land into the cache from the next read.
export function useBulkUpdateIssues() {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (updates: BulkUpdateItem[]) => issuesApi.bulkUpdate(workspaceId, updates),
    onMutate: async (updates) => {
      // Cancel in-flight refetches so they don't clobber the
      // optimistic snapshot we're about to write.
      await qc.cancelQueries({ queryKey: ["issues"] });
      const snapshots: Array<[readonly unknown[], Issue[] | undefined]> = [];
      // Apply the patch across every cached issues list.
      qc.getQueriesData<Issue[]>({ queryKey: ["issues"] }).forEach(([key, value]) => {
        snapshots.push([key, value]);
        if (!value) return;
        const byID = new Map(updates.map((u) => [u.id, u]));
        const next = value.map((iss) => {
          const u = byID.get(iss.id);
          if (!u) return iss;
          return {
            ...iss,
            status: (u.status as Issue["status"]) ?? iss.status,
            sort_order: u.sort_order ?? iss.sort_order,
          };
        });
        qc.setQueryData(key, next);
      });
      return { snapshots };
    },
    onError: (err: Error, _vars, ctx) => {
      // Roll back to the pre-drop cache state and surface the
      // failure. The kanban view re-reads from cache on the next
      // render, so the card snaps back to its origin column.
      ctx?.snapshots.forEach(([key, value]) => qc.setQueryData(key, value));
      toast(err.message, "error");
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["issues"] });
    },
  });
}
