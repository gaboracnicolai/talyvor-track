import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { relationsApi } from "~/api/relations";
import type { RelationType } from "~/api/types";
import { useWorkspace } from "./useWorkspace";
import { useUIStore } from "~/stores/ui";

// One file for the relation/dependency hooks. The query key shape
// matches the URL hierarchy so any successful mutation can target
// the right cache entries without ambiguity.

export function useRelations(issueID: string | null) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["relations", workspaceId, issueID],
    queryFn: () => relationsApi.list(workspaceId, issueID!),
    enabled: !!issueID && !!workspaceId,
  });
}

export function useDependencyGraph(issueID: string | null, depth = 3) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["dependency-graph", workspaceId, issueID, depth],
    queryFn: () => relationsApi.graph(workspaceId, issueID!, depth),
    enabled: !!issueID && !!workspaceId,
  });
}

export function useCreateRelation(issueID: string) {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (vars: { targetID: string; type: RelationType }) =>
      relationsApi.create(workspaceId, issueID, vars.targetID, vars.type),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["relations", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issue", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issues"] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useDeleteRelation(issueID: string) {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (vars: { targetID: string; type: RelationType }) =>
      relationsApi.remove(workspaceId, issueID, vars.targetID, vars.type),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["relations", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issue", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issues"] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useBulkCreateRelations(issueID: string) {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (vars: { targetIDs: string[]; type: RelationType }) =>
      relationsApi.bulkCreate(workspaceId, issueID, vars.targetIDs, vars.type),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["relations", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issue", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issues"] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useBlockingIssues(cycleID?: string) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["blocking", workspaceId, cycleID ?? null],
    queryFn: () => relationsApi.blocking(workspaceId, cycleID),
    enabled: !!workspaceId,
  });
}
