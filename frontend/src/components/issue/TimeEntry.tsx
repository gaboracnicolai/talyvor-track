import { X, DollarSign } from "lucide-react";
import type { TimeEntryRecord } from "~/api/types";
import { formatDuration } from "~/hooks/useTimeTracking";
import { Avatar } from "~/components/ui/Avatar";

interface TimeEntryProps {
  entry: TimeEntryRecord;
  onDelete?: () => void;
}

// One row in the per-issue time-entries list. Renders as a compact
// "avatar | description | duration | billable | delete" strip so a
// dozen entries still fit inside the IssueDetail dialog.
export function TimeEntry({ entry, onDelete }: TimeEntryProps) {
  const startDate = new Date(entry.started_at);
  return (
    <div className="flex items-center gap-2 rounded-md border border-border bg-bg px-2 py-1.5 text-xs">
      <Avatar name={entry.member_id.slice(0, 2)} />
      <div className="flex-1 truncate">
        <div className="truncate">
          {entry.description || <span className="text-muted">(no description)</span>}
        </div>
        <div className="text-[10px] text-muted">
          {startDate.toLocaleString()}
        </div>
      </div>
      <span className="font-mono text-text">{formatDuration(entry.duration_sec)}</span>
      {entry.billable ? (
        <span
          title="Billable"
          className="inline-flex items-center text-status-done"
        >
          <DollarSign size={12} />
        </span>
      ) : (
        <span className="text-[10px] uppercase tracking-wider text-muted">
          internal
        </span>
      )}
      {onDelete ? (
        <button
          onClick={onDelete}
          title="Remove entry"
          className="text-muted hover:text-priority-urgent"
        >
          <X size={12} />
        </button>
      ) : null}
    </div>
  );
}
