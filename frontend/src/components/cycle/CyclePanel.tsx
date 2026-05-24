import { Sparkles, AlertTriangle } from "lucide-react";
import { useUIStore } from "~/stores/ui";
import { useBlockingIssues } from "~/hooks/useRelations";
import type { CycleVelocity } from "~/api/types";

interface CyclePanelProps {
  cycle: CycleVelocity;
}

const fmtPct = (n: number): string => `${Math.round(n * 100)}%`;
const fmtUSD = (n: number): string => `$${n.toFixed(2)}`;

export function CyclePanel({ cycle }: CyclePanelProps) {
  const onTrack = cycle.completion_rate >= 0.7;
  // Surface the cycle's top blockers right under the progress bar —
  // PMs catch them while they're already looking at completion %.
  const blockers = useBlockingIssues(cycle.cycle_id);
  const setSelectedId = useUIStore((s) => s.setSelectedIssueId);
  const top = (blockers.data ?? []).slice(0, 3);

  return (
    <div className="rounded-md border border-border bg-surface p-4">
      <div className="mb-2 flex items-center justify-between">
        <h4 className="text-sm font-semibold">{cycle.cycle_name}</h4>
        <span className="text-[10px] text-muted">
          {cycle.start_date.slice(0, 10)} → {cycle.end_date.slice(0, 10)}
        </span>
      </div>
      <div className="flex items-end justify-between gap-2">
        <div>
          <div className="font-mono text-2xl font-semibold">{fmtPct(cycle.completion_rate)}</div>
          <div className="text-xs text-muted">
            {cycle.completed} / {cycle.total} completed
          </div>
        </div>
        <div className="flex items-center gap-1 text-xs text-accent">
          <Sparkles size={12} />
          {fmtUSD(cycle.ai_cost_usd)}
        </div>
      </div>
      <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-bg">
        <div
          className={onTrack ? "h-full bg-status-done" : "h-full bg-priority-medium"}
          style={{ width: `${Math.min(100, cycle.completion_rate * 100)}%` }}
        />
      </div>

      {top.length > 0 ? (
        <div className="mt-3 border-t border-border pt-3">
          <div className="mb-1 flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider text-priority-urgent">
            <AlertTriangle size={10} />
            Top blockers
          </div>
          <div className="space-y-1">
            {top.map((b) => (
              <button
                key={b.id}
                onClick={() => setSelectedId(b.id)}
                className="flex w-full items-center gap-2 rounded px-1 py-0.5 text-left text-xs hover:bg-bg"
                title="Open issue to resolve blocker"
              >
                <span className="font-mono text-muted">{b.identifier}</span>
                <span className="flex-1 truncate">{b.title}</span>
                <span className="rounded-full border border-priority-urgent/40 bg-priority-urgent/10 px-1.5 text-[10px] font-medium text-priority-urgent">
                  blocks {b.blocks_count}
                </span>
              </button>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}
