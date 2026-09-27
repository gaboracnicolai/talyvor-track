import { apiRequest } from "./client";
import type { CyclePlan } from "./types";

export const cyclesApi = {
  plan(wsID: string, teamID: string, cycleID: string) {
    return apiRequest<CyclePlan>(
      `/v1/workspaces/${wsID}/teams/${teamID}/cycles/${cycleID}/plan`,
    );
  },
};
