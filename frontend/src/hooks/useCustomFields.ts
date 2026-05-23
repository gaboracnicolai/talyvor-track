import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { customFieldsApi } from "~/api/customFields";
import { useWorkspace } from "./useWorkspace";
import { useUIStore } from "~/stores/ui";

// Centralised data hooks for custom fields. The catalogue is per
// (workspace, team); values are per issue. Keeping these in one file
// makes it easy to invalidate the right cache key after a mutation.

export function useCustomFields(teamID?: string) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["custom-fields", workspaceId, teamID ?? null],
    queryFn: () => customFieldsApi.list(workspaceId, teamID),
    enabled: !!workspaceId,
  });
}

export function useSetCustomFieldValue() {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: ({
      issueID,
      fieldID,
      value,
    }: {
      issueID: string;
      fieldID: string;
      value: string;
    }) => customFieldsApi.setValue(workspaceId, issueID, fieldID, value),
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ["issue", workspaceId, vars.issueID] });
      qc.invalidateQueries({ queryKey: ["issues"] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
}
