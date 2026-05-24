import { useState } from "react";
import { BarChart3, Sparkles } from "lucide-react";
import clsx from "clsx";
import { useUIStore } from "~/stores/ui";
import {
  formatScore,
  usePrioritizedBacklog,
  useScoringSummary,
} from "~/hooks/useScoring";
import { useWorkspace } from "~/hooks/useWorkspace";
import { StatusBadge } from "~/components/issue/StatusBadge";
import { PriorityIcon } from "~/components/issue/PriorityIcon";
import { AICostBadge } from "~/components/issue/AICostBadge";
import { IssueDetail } from "~/components/issue/IssueDetail";
import type { ScoringMethod, ScoredIssue } from "~/api/types";

// Ranked backlog: top-N issues by RICE or ICE score with a "set
// score" affordance on every row. Unscored issues land at the bottom
// greyed out so PMs see at a glance how much of the backlog still
// needs prioritisation.
export function PrioritizedBacklogPage() {
  const { teamId } = useWorkspace();
  const [method, setMethod] = useState<ScoringMethod>("rice");
  const setSelectedId = useUIStore((s) => s.setSelectedIssueId);
  const selectedId = useUIStore((s) => s.selectedIssueId);

  const backlog = usePrioritizedBacklog(method, teamId || undefined);
  const summary = useScoringSummary();

  return (
    <div className="flex h-full flex-col">
      <header className="flex items-center justify-between border-b border-border bg-surface px-4 py-3">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <BarChart3 size={14} className="text-accent" />
          Prioritized backlog
        </div>
        <MethodToggle method={method} setMethod={setMethod} />
      </header>

      {summary.data ? (
        <div className="grid grid-cols-2 gap-2 border-b border-border bg-surface/60 px-4 py-2 sm:grid-cols-4">
          <Kpi label="Coverage" value={`${Math.round(summary.data.coverage_pct)}%`} />
          <Kpi label="Scored" value={`${summary.data.total_scored} / ${summary.data.total_issues}`} />
          <Kpi label="Avg RICE" value={summary.data.avg_rice_score.toFixed(1)} />
          <Kpi label="Avg ICE" value={Math.round(summary.data.avg_ice_score).toString()} />
        </div>
      ) : null}

      <main className="flex-1 overflow-y-auto">
        {backlog.isLoading ? (
          <div className="p-8 text-center text-sm text-muted">Loading…</div>
        ) : (backlog.data ?? []).length === 0 ? (
          <div className="p-8 text-center text-sm text-muted">No issues to prioritise.</div>
        ) : (
          (backlog.data ?? []).map((iss) => (
            <Row
              key={iss.id}
              issue={iss}
              method={method}
              onOpen={() => setSelectedId(iss.id)}
            />
          ))
        )}
      </main>

      <IssueDetail issueId={selectedId} onClose={() => setSelectedId(null)} />
    </div>
  );
}

function MethodToggle({
  method,
  setMethod,
}: {
  method: ScoringMethod;
  setMethod: (m: ScoringMethod) => void;
}) {
  return (
    <div className="inline-flex rounded-md border border-border bg-bg p-0.5">
      {(["rice", "ice"] as ScoringMethod[]).map((m) => (
        <button
          key={m}
          onClick={() => setMethod(m)}
          className={clsx(
            "h-7 rounded px-3 text-xs",
            method === m ? "bg-surface text-text" : "text-muted hover:text-text",
          )}
        >
          {m.toUpperCase()}
        </button>
      ))}
    </div>
  );
}

function Kpi({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wider text-muted">{label}</div>
      <div className="font-mono text-sm">{value}</div>
    </div>
  );
}

function Row({
  issue,
  method,
  onOpen,
}: {
  issue: ScoredIssue;
  method: ScoringMethod;
  onOpen: () => void;
}) {
  const unscored = issue.score_rank === 0;
  return (
    <button
      onClick={onOpen}
      className={clsx(
        "flex w-full items-center gap-3 border-b border-border px-4 py-2 text-left hover:bg-surface",
        unscored ? "opacity-50" : "",
      )}
    >
      <span className="w-8 shrink-0 text-right font-mono text-xs text-muted">
        {unscored ? "—" : `#${issue.score_rank}`}
      </span>
      <PriorityIcon priority={issue.priority} />
      <span className="w-20 shrink-0 font-mono text-xs text-muted">{issue.identifier}</span>
      <StatusBadge status={issue.status} />
      <span className="flex-1 truncate text-sm">{issue.title}</span>
      {unscored ? (
        <span className="text-[10px] uppercase tracking-wider text-muted">
          unscored
        </span>
      ) : (
        <span
          className="inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-[10px] font-medium text-accent"
          title={`${method.toUpperCase()} score`}
        >
          <Sparkles size={10} />
          {method.toUpperCase()} {formatScore(issue.score, method)}
        </span>
      )}
      <AICostBadge costUSD={issue.ai_cost_usd ?? 0} tokens={issue.ai_tokens ?? 0} />
    </button>
  );
}
