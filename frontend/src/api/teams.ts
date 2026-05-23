import { apiRequest } from "./client";
import type { Team } from "./types";

export const teamsApi = {
  list(wsID: string) {
    return apiRequest<Team[]>(`/v1/workspaces/${wsID}/teams`);
  },
  get(wsID: string, id: string) {
    return apiRequest<Team>(`/v1/workspaces/${wsID}/teams/${id}`);
  },
};
