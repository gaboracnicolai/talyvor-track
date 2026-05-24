import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { templatesApi } from "~/api/templates";
import type { IssueTemplate } from "~/api/types";
import { useWorkspace } from "./useWorkspace";
import { useUIStore } from "~/stores/ui";

const key = "templates";

export function useTemplates(teamID?: string) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: [key, workspaceId, teamID ?? null],
    queryFn: () => templatesApi.list(workspaceId, teamID),
    enabled: !!workspaceId,
  });
}

export function useCreateTemplate() {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (body: Partial<IssueTemplate>) => templatesApi.create(workspaceId, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: [key] }),
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useUpdateTemplate() {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (vars: { id: string; updates: Partial<IssueTemplate> }) =>
      templatesApi.update(workspaceId, vars.id, vars.updates),
    onSuccess: () => qc.invalidateQueries({ queryKey: [key] }),
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useDeleteTemplate() {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (id: string) => templatesApi.remove(workspaceId, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: [key] }),
    onError: (err: Error) => toast(err.message, "error"),
  });
}
