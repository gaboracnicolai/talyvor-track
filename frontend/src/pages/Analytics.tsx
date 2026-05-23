import { useQuery } from "@tanstack/react-query";
import { analyticsApi } from "~/api/analytics";
import { useWorkspace } from "~/hooks/useWorkspace";
import { AICostChart } from "~/components/analytics/AICostChart";
import { VelocityChart } from "~/components/analytics/VelocityChart";
import { WorkloadView } from "~/components/analytics/WorkloadView";

export function AnalyticsPage() {
  const { workspaceId, teamId } = useWorkspace();
  const trends = useQuery({
    queryKey: ["ai-costs", workspaceId],
    queryFn: () => analyticsApi.aiCosts(workspaceId, 30),
    enabled: !!workspaceId,
  });
  const velocity = useQuery({
    queryKey: ["velocity", workspaceId, teamId],
    queryFn: () => analyticsApi.velocity(workspaceId, teamId, 6),
    enabled: !!workspaceId && !!teamId,
  });
  const workload = useQuery({
    queryKey: ["workload", workspaceId, teamId],
    queryFn: () => analyticsApi.workload(workspaceId, teamId || undefined),
    enabled: !!workspaceId,
  });

  return (
    <div className="space-y-4 p-4">
      {trends.data ? <AICostChart trends={trends.data} /> : <Skeleton />}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <div className="rounded-md border border-border bg-surface p-4">
          <h3 className="mb-3 text-sm font-semibold">Velocity</h3>
          {velocity.data && velocity.data.length > 0 ? (
            <VelocityChart cycles={velocity.data} />
          ) : (
            <p className="text-xs text-muted">No cycles to chart.</p>
          )}
        </div>
        {workload.data ? <WorkloadView workload={workload.data} /> : <Skeleton />}
      </div>
    </div>
  );
}

function Skeleton() {
  return <div className="h-48 animate-pulse rounded-md border border-border bg-surface" />;
}
