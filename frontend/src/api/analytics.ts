import { apiRequest, qs } from "./client";
import type {
  AICostTrends,
  BurndownReport,
  CycleVelocity,
  MemberWorkload,
} from "./types";

export const analyticsApi = {
  velocity(wsID: string, teamID: string, cycles = 5) {
    return apiRequest<CycleVelocity[]>(
      `/v1/workspaces/${wsID}/analytics/velocity${qs({ team_id: teamID, cycles })}`,
    );
  },
  burndown(wsID: string, cycleID: string) {
    return apiRequest<BurndownReport>(
      `/v1/workspaces/${wsID}/analytics/burndown${qs({ cycle_id: cycleID })}`,
    );
  },
  aiCosts(wsID: string, days = 30) {
    return apiRequest<AICostTrends>(
      `/v1/workspaces/${wsID}/analytics/ai-costs${qs({ days })}`,
    );
  },
  workload(wsID: string, teamID?: string) {
    return apiRequest<MemberWorkload[]>(
      `/v1/workspaces/${wsID}/analytics/workload${qs({ team_id: teamID })}`,
    );
  },
  distribution(wsID: string, groupBy: string, days = 30) {
    return apiRequest<
      Array<{ label: string; count: number; pct: number; ai_cost_usd: number }>
    >(
      `/v1/workspaces/${wsID}/analytics/distribution${qs({ group_by: groupBy, days })}`,
    );
  },
};
