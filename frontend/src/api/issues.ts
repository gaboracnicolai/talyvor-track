import { apiRequest, qs } from "./client";
import type { Issue } from "./types";

export interface ListIssuesParams {
  team_id?: string;
  status?: string;
  assignee_id?: string;
  priority?: number;
  limit?: number;
  offset?: number;
  order_by?: string;
  order_dir?: string;
}

export const issuesApi = {
  list(wsID: string, params: ListIssuesParams = {}) {
    return apiRequest<Issue[]>(
      `/v1/workspaces/${wsID}/issues${qs(params as Record<string, string | number | undefined>)}`,
    );
  },
  get(wsID: string, id: string) {
    return apiRequest<Issue>(`/v1/workspaces/${wsID}/issues/${id}`);
  },
  search(wsID: string, q: string) {
    return apiRequest<Issue[]>(`/v1/workspaces/${wsID}/issues/search${qs({ q })}`);
  },
  create(wsID: string, body: Partial<Issue>) {
    return apiRequest<Issue>(`/v1/workspaces/${wsID}/issues`, {
      method: "POST",
      body,
    });
  },
  update(wsID: string, id: string, updates: Record<string, unknown>) {
    return apiRequest<Issue>(`/v1/workspaces/${wsID}/issues/${id}`, {
      method: "PATCH",
      body: updates,
    });
  },
  remove(wsID: string, id: string) {
    return apiRequest<{ ok: boolean }>(`/v1/workspaces/${wsID}/issues/${id}`, {
      method: "DELETE",
    });
  },
  semanticSearch(wsID: string, q: string, limit = 25) {
    return apiRequest<Issue[]>(
      `/v1/workspaces/${wsID}/issues/semantic-search${qs({ q, limit })}`,
    );
  },
};
