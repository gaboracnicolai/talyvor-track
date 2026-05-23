import { create } from "zustand";

interface WorkspaceState {
  workspaceId: string;
  memberId: string;
  teamId: string;
  setWorkspaceId: (id: string) => void;
  setMemberId: (id: string) => void;
  setTeamId: (id: string) => void;
}

// Workspace + member context is held in Zustand so any component can
// reach it without prop drilling. Defaults come from localStorage so
// switching workspaces survives a refresh. No login flow yet — Phase
// 9 will replace this with a real auth-driven session.
export const useWorkspaceStore = create<WorkspaceState>((set) => ({
  workspaceId: localStorage.getItem("track_workspace_id") ?? "default",
  memberId: localStorage.getItem("track_member_id") ?? "",
  teamId: localStorage.getItem("track_team_id") ?? "",
  setWorkspaceId: (id) => {
    localStorage.setItem("track_workspace_id", id);
    set({ workspaceId: id });
  },
  setMemberId: (id) => {
    localStorage.setItem("track_member_id", id);
    set({ memberId: id });
  },
  setTeamId: (id) => {
    localStorage.setItem("track_team_id", id);
    set({ teamId: id });
  },
}));
