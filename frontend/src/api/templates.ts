import { apiRequest, qs } from "./client";
import type { IssueTemplate } from "./types";

export const templatesApi = {
  list(wsID: string, teamID?: string) {
    return apiRequest<IssueTemplate[]>(
      `/v1/workspaces/${wsID}/templates${qs({ team_id: teamID })}`,
    );
  },
  get(wsID: string, id: string) {
    return apiRequest<IssueTemplate>(`/v1/workspaces/${wsID}/templates/${id}`);
  },
  create(wsID: string, body: Partial<IssueTemplate>) {
    return apiRequest<IssueTemplate>(`/v1/workspaces/${wsID}/templates`, {
      method: "POST",
      body,
    });
  },
  update(wsID: string, id: string, body: Partial<IssueTemplate>) {
    return apiRequest<IssueTemplate>(`/v1/workspaces/${wsID}/templates/${id}`, {
      method: "PATCH",
      body,
    });
  },
  remove(wsID: string, id: string) {
    return apiRequest<{ ok: boolean }>(`/v1/workspaces/${wsID}/templates/${id}`, {
      method: "DELETE",
    });
  },
};
