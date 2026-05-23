import type { MemberWorkload } from "~/api/types";
import { Avatar } from "~/components/ui/Avatar";
import { Sparkles } from "lucide-react";

interface WorkloadViewProps {
  workload: MemberWorkload[];
}

const fmtUSD = (n: number): string => `$${n.toFixed(2)}`;

export function WorkloadView({ workload }: WorkloadViewProps) {
  if (workload.length === 0) {
    return (
      <div className="rounded-md border border-border bg-surface p-6 text-center text-sm text-muted">
        No workload data yet.
      </div>
    );
  }
  const maxOpen = Math.max(...workload.map((w) => w.open_issues), 1);
  return (
    <div className="space-y-2 rounded-md border border-border bg-surface p-4">
      <h3 className="mb-3 text-sm font-semibold">Workload by member</h3>
      {workload.map((m) => (
        <div key={m.member_id} className="flex items-center gap-3">
          <Avatar name={m.name} url={m.avatar_url} />
          <div className="flex-1">
            <div className="flex items-center justify-between text-xs">
              <span className="font-medium">{m.name}</span>
              <span className="text-muted">
                {m.open_issues} open · {m.in_progress} in-progress
                {m.overdue > 0 ? (
                  <span className="ml-1 text-priority-urgent">· {m.overdue} overdue</span>
                ) : null}
              </span>
            </div>
            <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-bg">
              <div
                className="h-full bg-accent"
                style={{ width: `${(m.open_issues / maxOpen) * 100}%` }}
              />
            </div>
          </div>
          {m.ai_cost_usd > 0 ? (
            <span className="flex items-center gap-1 text-xs text-accent">
              <Sparkles size={10} />
              {fmtUSD(m.ai_cost_usd)}
            </span>
          ) : null}
        </div>
      ))}
    </div>
  );
}
