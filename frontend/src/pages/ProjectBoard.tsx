import { useMemo } from "react";
import { useIssues } from "~/hooks/useIssues";
import { useUIStore } from "~/stores/ui";
import { StatusBadge } from "~/components/issue/StatusBadge";
import { PriorityIcon } from "~/components/issue/PriorityIcon";
import { AICostBadge } from "~/components/issue/AICostBadge";
import { IssueDetail } from "~/components/issue/IssueDetail";
import type { Issue, IssueStatus } from "~/api/types";

const columns: { status: IssueStatus; title: string }[] = [
  { status: "backlog", title: "Backlog" },
  { status: "todo", title: "Todo" },
  { status: "in_progress", title: "In Progress" },
  { status: "in_review", title: "In Review" },
  { status: "done", title: "Done" },
];

export function ProjectBoardPage() {
  const { data, isLoading } = useIssues();
  const selectedId = useUIStore((s) => s.selectedIssueId);
  const setSelectedId = useUIStore((s) => s.setSelectedIssueId);

  const grouped = useMemo(() => {
    const out: Record<IssueStatus, Issue[]> = {
      backlog: [],
      todo: [],
      in_progress: [],
      in_review: [],
      done: [],
      cancelled: [],
    };
    for (const issue of data ?? []) out[issue.status]?.push(issue);
    return out;
  }, [data]);

  return (
    <>
      <div className="flex h-full gap-3 overflow-x-auto p-4">
        {columns.map((col) => (
          <div key={col.status} className="flex h-full w-72 shrink-0 flex-col">
            <div className="mb-2 flex items-center justify-between px-1">
              <div className="flex items-center gap-2 text-xs font-semibold">
                <StatusBadge status={col.status} />
                {col.title}
              </div>
              <span className="text-[10px] text-muted">{grouped[col.status].length}</span>
            </div>
            <div className="flex-1 space-y-2 overflow-y-auto pr-1">
              {isLoading ? (
                <div className="text-xs text-muted">Loading…</div>
              ) : (
                grouped[col.status].map((issue) => (
                  <Card key={issue.id} issue={issue} onClick={() => setSelectedId(issue.id)} />
                ))
              )}
            </div>
          </div>
        ))}
      </div>
      <IssueDetail issueId={selectedId} onClose={() => setSelectedId(null)} />
    </>
  );
}

function Card({ issue, onClick }: { issue: Issue; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className="w-full rounded-md border border-border bg-surface p-3 text-left hover:border-border/80"
    >
      <div className="mb-1 flex items-center gap-2 text-[10px] text-muted">
        <span className="font-mono">{issue.identifier}</span>
        <PriorityIcon priority={issue.priority} />
        <AICostBadge costUSD={issue.ai_cost_usd ?? 0} tokens={issue.ai_tokens ?? 0} />
      </div>
      <div className="text-sm">{issue.title}</div>
    </button>
  );
}
