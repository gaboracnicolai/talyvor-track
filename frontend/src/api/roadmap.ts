import { apiRequest, qs } from "./client";
import type { RoadmapResponse } from "./types";

export interface RoadmapParams {
  start_date?: string;
  end_date?: string;
  team_id?: string;
}

export const roadmapApi = {
  get(wsID: string, params: RoadmapParams = {}) {
    return apiRequest<RoadmapResponse>(
      `/v1/workspaces/${wsID}/roadmap${qs(params as Record<string, string | number | undefined>)}`,
    );
  },
};
