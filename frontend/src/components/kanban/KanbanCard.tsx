import { forwardRef } from "react";
import { AlertCircle } from "lucide-react";
import clsx from "clsx";
import type { Issue } from "~/api/types";
import { PriorityIcon } from "~/components/issue/PriorityIcon";
import { AICostBadge } from "~/components/issue/AICostBadge";
import { Avatar } from "~/components/ui/Avatar";

interface KanbanCardProps {
  issue: Issue;
  focused?: boolean;
  dragging?: boolean;
  onClick: () => void;
  // Pointer-event hooks bubble up to the column / board so the parent
  // can manage drag lifecycle (state, drop targets, animation).
  onPointerDown?: (e: React.PointerEvent<HTMLDivElement>) => void;
}

// One issue in the kanban view. Keep this component dumb — it owns
// no drag state, only renders. The board orchestrates pointer events.
export const KanbanCard = forwardRef<HTMLDivElement, KanbanCardProps>(
  ({ issue, focused, dragging, onClick, onPointerDown }, ref) => {
    return (
      <div
        ref={ref}
        onClick={onClick}
        onPointerDown={onPointerDown}
        data-issue-id={issue.id}
        className={clsx(
          "select-none rounded-md border bg-surface p-3 text-left transition-shadow",
          // touch-none lets pointer events fire on mobile without the
          // browser interpreting the gesture as a scroll.
          "touch-none",
          // Red left border calls out blocked cards at a glance —
          // sprint planning's primary signal that this column has
          // load it can't progress.
          issue.is_blocked ? "border-l-2 border-l-priority-urgent" : "",
          dragging
            ? "cursor-grabbing border-accent opacity-50 shadow-lg"
            : "cursor-grab border-border hover:border-border/80 hover:shadow",
          focused ? "ring-1 ring-inset ring-accent" : "",
        )}
      >
        <div className="mb-1 flex items-center gap-2 text-[10px] text-muted">
          <PriorityIcon priority={issue.priority} />
          <span className="font-mono">{issue.identifier}</span>
          {issue.is_blocked ? (
            <AlertCircle
              size={12}
              className="text-priority-urgent"
              aria-label="Blocked"
            />
          ) : null}
          <span className="ml-auto">
            <AICostBadge costUSD={issue.ai_cost_usd ?? 0} tokens={issue.ai_tokens ?? 0} />
          </span>
        </div>
        <div className="line-clamp-2 text-sm text-text">{issue.title}</div>
        {issue.assignee_id ? (
          <div className="mt-2 flex items-center justify-end">
            <Avatar name={issue.assignee_id.slice(0, 2)} />
          </div>
        ) : null}
      </div>
    );
  },
);
KanbanCard.displayName = "KanbanCard";
