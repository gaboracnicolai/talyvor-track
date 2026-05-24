import { useQuery } from "@tanstack/react-query";
import { roadmapApi, type RoadmapParams } from "~/api/roadmap";
import { useWorkspace } from "./useWorkspace";

// useRoadmap fetches the per-workspace roadmap. The query key keeps
// the entire params object so any change to date range or team
// filter buckets to a fresh cache entry.
export function useRoadmap(params: RoadmapParams = {}) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["roadmap", workspaceId, params],
    queryFn: () => roadmapApi.get(workspaceId, params),
    enabled: !!workspaceId,
  });
}
