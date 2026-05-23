import { apiRequest } from "./client";
import type { Project } from "./types";

export const projectsApi = {
  list(wsID: string) {
    return apiRequest<Project[]>(`/v1/workspaces/${wsID}/projects`);
  },
  get(wsID: string, id: string) {
    return apiRequest<Project>(`/v1/workspaces/${wsID}/projects/${id}`);
  },
};
