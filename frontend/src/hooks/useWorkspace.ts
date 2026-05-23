import { useWorkspaceStore } from "../stores/workspace";

// Thin selector hook. Most components only need the workspaceId; this
// makes the call site one line and keeps the import surface small.
export function useWorkspace() {
  return useWorkspaceStore((s) => ({
    workspaceId: s.workspaceId,
    memberId: s.memberId,
    teamId: s.teamId,
  }));
}
