import { Circle, CircleDot, CircleDashed, CircleCheck, CircleX } from "lucide-react";
import type { IssueStatus } from "~/api/types";
import clsx from "clsx";

const config: Record<IssueStatus, { label: string; Icon: typeof Circle; color: string }> = {
  backlog: { label: "Backlog", Icon: CircleDashed, color: "text-status-backlog" },
  todo: { label: "Todo", Icon: Circle, color: "text-status-todo" },
  in_progress: { label: "In Progress", Icon: CircleDot, color: "text-status-in_progress" },
  in_review: { label: "In Review", Icon: CircleDot, color: "text-status-in_progress" },
  done: { label: "Done", Icon: CircleCheck, color: "text-status-done" },
  cancelled: { label: "Cancelled", Icon: CircleX, color: "text-status-cancelled" },
};

interface StatusBadgeProps {
  status: IssueStatus;
  withLabel?: boolean;
  className?: string;
}

export function StatusBadge({ status, withLabel, className }: StatusBadgeProps) {
  const c = config[status] ?? config.backlog;
  const Icon = c.Icon;
  return (
    <span className={clsx("inline-flex items-center gap-1.5", c.color, className)}>
      <Icon size={14} strokeWidth={2} />
      {withLabel ? <span className="text-xs">{c.label}</span> : null}
    </span>
  );
}
