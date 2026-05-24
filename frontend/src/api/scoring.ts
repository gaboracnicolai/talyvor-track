import { apiRequest, qs } from "./client";
import type {
  ICEScore,
  IssueScoreRecord,
  RICEScore,
  ScoredIssue,
  ScoreSummary,
  ScoringMethod,
} from "./types";

export const scoringApi = {
  getScore(wsID: string, issueID: string) {
    return apiRequest<IssueScoreRecord>(
      `/v1/workspaces/${wsID}/issues/${issueID}/score`,
    );
  },
  setScore(
    wsID: string,
    issueID: string,
    body: {
      method: ScoringMethod;
      rice?: Partial<RICEScore>;
      ice?: Partial<ICEScore>;
      notes?: string;
    },
  ) {
    return apiRequest<IssueScoreRecord>(
      `/v1/workspaces/${wsID}/issues/${issueID}/score`,
      { method: "PUT", body },
    );
  },
  deleteScore(wsID: string, issueID: string) {
    return apiRequest<{ ok: boolean }>(
      `/v1/workspaces/${wsID}/issues/${issueID}/score`,
      { method: "DELETE" },
    );
  },
  prioritized(
    wsID: string,
    params: { method?: ScoringMethod; team_id?: string; limit?: number } = {},
  ) {
    return apiRequest<ScoredIssue[]>(
      `/v1/workspaces/${wsID}/backlog/prioritized${qs(params as Record<string, string | number | undefined>)}`,
    );
  },
  summary(wsID: string) {
    return apiRequest<ScoreSummary>(`/v1/workspaces/${wsID}/scoring/summary`);
  },
};
