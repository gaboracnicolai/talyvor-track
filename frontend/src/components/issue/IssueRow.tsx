import clsx from "clsx";
import { AlertCircle } from "lucide-react";
import type { Issue } from "~/api/types";
import { StatusBadge } from "./StatusBadge";
import { PriorityIcon } from "./PriorityIcon";
import { AICostBadge } from "./AICostBadge";
import { Avatar } from "~/components/ui/Avatar";
import { Badge } from "~/components/ui/Badge";
import { Tooltip } from "~/components/ui/Tooltip";

interface IssueRowProps {
  issue: Issue;
  focused?: boolean;
  selected?: boolean;
  onClick: () => void;
}

const relativeTime = (iso: string): string => {
  const ms = Date.now() - new Date(iso).getTime();
  const days = Math.floor(ms / 86_400_000);
  if (days <= 0) return "today";
  if (days === 1) return "yesterday";
  if (days < 30) return `${days}d`;
  return `${Math.floor(days / 30)}mo`;
};

export function IssueRow({ issue, focused, selected, onClick }: IssueRowProps) {
  return (
    <button
      data-issue-id={issue.id}
      onClick={onClick}
      className={clsx(
        "flex w-full items-center gap-3 border-b border-border px-4 py-2 text-left",
        focused ? "bg-bg" : "hover:bg-bg/60",
        selected ? "ring-1 ring-inset ring-accent" : "",
      )}
    >
      <PriorityIcon priority={issue.priority} />
      <span className="w-20 shrink-0 font-mono text-xs text-muted">{issue.identifier}</span>
      <StatusBadge status={issue.status} />
      {issue.is_blocked ? (
        <Tooltip content="Blocked by other issues">
          <span className="inline-flex">
            <AlertCircle size={12} className="text-priority-urgent" />
          </span>
        </Tooltip>
      ) : null}
      <span className="flex-1 truncate text-sm">{issue.title}</span>
      <AICostBadge costUSD={issue.ai_cost_usd ?? 0} tokens={issue.ai_tokens ?? 0} />
      <div className="hidden gap-1 md:flex">
        {(issue.labels ?? []).slice(0, 2).map((l) => (
          <Badge key={l}>{l}</Badge>
        ))}
      </div>
      <span className="hidden w-12 shrink-0 text-right text-xs text-muted md:inline">
        {relativeTime(issue.updated_at)}
      </span>
      {issue.assignee_id ? (
        <Avatar name={issue.assignee_id.slice(0, 2)} />
      ) : (
        <div className="h-6 w-6 rounded-full border border-dashed border-border" />
      )}
    </button>
  );
}
