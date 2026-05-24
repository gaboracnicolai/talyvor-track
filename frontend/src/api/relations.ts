import { apiRequest, qs } from "./client";
import type {
  BlockingIssue,
  DependencyGraph,
  RelationStats,
  RelationType,
  RelationWithIssue,
} from "./types";

export const relationsApi = {
  list(wsID: string, issueID: string) {
    return apiRequest<RelationWithIssue[]>(
      `/v1/workspaces/${wsID}/issues/${issueID}/relations`,
    );
  },
  create(wsID: string, issueID: string, targetID: string, type: RelationType) {
    return apiRequest(`/v1/workspaces/${wsID}/issues/${issueID}/relations`, {
      method: "POST",
      body: { target_id: targetID, type },
    });
  },
  remove(wsID: string, issueID: string, targetID: string, type: RelationType) {
    return apiRequest(`/v1/workspaces/${wsID}/issues/${issueID}/relations`, {
      method: "DELETE",
      body: { target_id: targetID, type },
    });
  },
  graph(wsID: string, issueID: string, depth = 3) {
    return apiRequest<DependencyGraph>(
      `/v1/workspaces/${wsID}/issues/${issueID}/dependency-graph${qs({ depth })}`,
    );
  },
  stats(wsID: string) {
    return apiRequest<RelationStats>(`/v1/workspaces/${wsID}/relations/stats`);
  },
  blocking(wsID: string, cycleID?: string) {
    return apiRequest<BlockingIssue[]>(
      `/v1/workspaces/${wsID}/relations/blocking${qs({ cycle_id: cycleID })}`,
    );
  },
  bulkCreate(wsID: string, issueID: string, targetIDs: string[], type: RelationType) {
    return apiRequest<{ created: number }>(
      `/v1/workspaces/${wsID}/issues/${issueID}/relations/bulk`,
      { method: "POST", body: { target_ids: targetIDs, type } },
    );
  },
};
