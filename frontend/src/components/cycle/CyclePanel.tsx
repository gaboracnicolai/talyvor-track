import type { CycleVelocity } from "~/api/types";
import { Sparkles } from "lucide-react";

interface CyclePanelProps {
  cycle: CycleVelocity;
}

const fmtPct = (n: number): string => `${Math.round(n * 100)}%`;
const fmtUSD = (n: number): string => `$${n.toFixed(2)}`;

export function CyclePanel({ cycle }: CyclePanelProps) {
  const onTrack = cycle.completion_rate >= 0.7;
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
    </div>
  );
}
