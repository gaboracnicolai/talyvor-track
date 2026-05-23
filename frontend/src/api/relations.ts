import { apiRequest, qs } from "./client";
import type {
  DependencyGraph,
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
};
