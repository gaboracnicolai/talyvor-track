import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { issuesApi, type ListIssuesParams } from "../api/issues";
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
