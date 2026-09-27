import { useShallow } from "zustand/react/shallow";
import { useWorkspaceStore } from "../stores/workspace";

// Thin selector hook. Most components only need the workspaceId; this
// makes the call site one line and keeps the import surface small.
// useShallow keeps the returned object stable while its fields are
// unchanged — zustand 5 re-renders forever on a selector that builds a
// new object every call, which crashed the whole shell in <Sidebar>.
export function useWorkspace() {
  return useWorkspaceStore(
    useShallow((s) => ({
      workspaceId: s.workspaceId,
      memberId: s.memberId,
      teamId: s.teamId,
    })),
  );
}
