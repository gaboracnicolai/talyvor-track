import { AlertTriangle } from "lucide-react";

interface BlockerAlertProps {
  count: number;
  onClick?: () => void;
}

// Shown at the top of a kanban column when at least one of its
// cards is blocked. Click to scroll-pulse the blocked cards (the
// board component handles the side-effect; this component just
// renders the affordance).
export function BlockerAlert({ count, onClick }: BlockerAlertProps) {
  if (count < 1) return null;
  return (
    <button
      onClick={onClick}
      className="flex w-full items-center gap-1 rounded-md border border-priority-urgent/40 bg-priority-urgent/10 px-2 py-1 text-[10px] font-medium text-priority-urgent hover:bg-priority-urgent/15"
    >
      <AlertTriangle size={10} />
      {count === 1
        ? "1 issue in this column is blocked"
        : `${count} issues in this column are blocked`}
    </button>
  );
}
