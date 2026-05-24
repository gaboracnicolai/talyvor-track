import { Plus } from "lucide-react";
import clsx from "clsx";
import { StatusBadge } from "~/components/issue/StatusBadge";
import { KanbanCard } from "./KanbanCard";
import { BlockerAlert } from "./BlockerAlert";
import type { Issue, IssueStatus } from "~/api/types";

interface KanbanColumnProps {
  status: IssueStatus;
  title: string;
  issues: Issue[];
  focusedId: string | null;
  draggingId: string | null;
  isDropTarget: boolean;
  onCardClick: (id: string) => void;
  onCardPointerDown: (issue: Issue, e: React.PointerEvent<HTMLDivElement>) => void;
  onCreate?: () => void;
}

// One status column. The board owns drag state and tells the column
// whether it's the current drop target via isDropTarget. Cards are
// rendered in sort_order ascending — that's the order the bulk
// update gives them, so the visual matches the back-end source of
// truth.
export function KanbanColumn({
  status,
  title,
  issues,
  focusedId,
  draggingId,
  isDropTarget,
  onCardClick,
  onCardPointerDown,
  onCreate,
}: KanbanColumnProps) {
  return (
    <div
      data-status={status}
      data-kanban-column
      className={clsx(
        "flex h-full w-72 shrink-0 flex-col rounded-md border bg-surface/40 transition-colors",
        isDropTarget ? "border-accent" : "border-border",
      )}
    >
      <div className="flex items-center justify-between border-b border-border px-3 py-2">
        <div className="flex items-center gap-2 text-xs font-semibold">
          <StatusBadge status={status} />
          <span>{title}</span>
        </div>
        <span className="text-[10px] text-muted">{issues.length}</span>
      </div>

      <div
        className="flex-1 space-y-2 overflow-y-auto p-2"
        // Set a min height even when empty so the user has something
        // to drop onto. The dashed-border treatment for empty columns
        // is rendered below.
        style={{ minHeight: 64 }}
      >
        <BlockerAlert count={issues.filter((i) => i.is_blocked).length} />
        {issues.length === 0 ? (
          <div className="flex h-24 items-center justify-center rounded-md border border-dashed border-border text-[10px] text-muted">
            Drop issues here
          </div>
        ) : (
          issues.map((issue) => (
            <KanbanCard
              key={issue.id}
              issue={issue}
              focused={issue.id === focusedId}
              dragging={issue.id === draggingId}
              onClick={() => onCardClick(issue.id)}
              onPointerDown={(e) => onCardPointerDown(issue, e)}
            />
          ))
        )}
      </div>

      {onCreate ? (
        <button
          onClick={onCreate}
          className="flex items-center gap-1 border-t border-border px-3 py-2 text-xs text-muted hover:bg-bg hover:text-text"
        >
          <Plus size={12} /> Add issue
        </button>
      ) : null}
    </div>
  );
}
