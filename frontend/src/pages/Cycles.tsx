import { useQuery } from "@tanstack/react-query";
import { analyticsApi } from "~/api/analytics";
import { useWorkspace } from "~/hooks/useWorkspace";
import { CyclePanel } from "~/components/cycle/CyclePanel";
import { BurndownChart } from "~/components/cycle/BurndownChart";

export function CyclesPage() {
  const { workspaceId, teamId } = useWorkspace();
  const velocity = useQuery({
    queryKey: ["velocity", workspaceId, teamId],
    queryFn: () => analyticsApi.velocity(workspaceId, teamId, 6),
    enabled: !!workspaceId && !!teamId,
  });
  const activeCycle = velocity.data?.[0];
  const burndown = useQuery({
    queryKey: ["burndown", workspaceId, activeCycle?.cycle_id],
    queryFn: () => analyticsApi.burndown(workspaceId, activeCycle!.cycle_id),
    enabled: !!activeCycle,
  });

  if (!teamId) {
    return <EmptyState message="Pick a team in Settings to see cycles." />;
  }
  if (velocity.isLoading) {
    return <div className="p-8 text-center text-sm text-muted">Loading cycles…</div>;
  }
  if (!velocity.data || velocity.data.length === 0) {
    return <EmptyState message="No cycles found for this team yet." />;
  }
  return (
    <div className="space-y-4 p-4">
      {burndown.data ? <BurndownChart report={burndown.data} /> : null}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
        {velocity.data.map((c) => (
          <CyclePanel key={c.cycle_id} cycle={c} />
        ))}
      </div>
    </div>
  );
}

function EmptyState({ message }: { message: string }) {
  return (
    <div className="flex h-64 items-center justify-center text-sm text-muted">{message}</div>
  );
}
