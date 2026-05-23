import type { IssueStatus, IssuePriority } from "~/api/types";
import { StatusBadge } from "./StatusBadge";
import { PriorityIcon } from "./PriorityIcon";
import clsx from "clsx";

export interface IssueFilterValue {
  status?: IssueStatus | "all";
  priority?: IssuePriority | "all";
}

interface IssueFiltersProps {
  value: IssueFilterValue;
  onChange: (v: IssueFilterValue) => void;
}

const statuses: (IssueStatus | "all")[] = ["all", "backlog", "todo", "in_progress", "done"];
const priorities: (IssuePriority | "all")[] = ["all", 1, 2, 3, 4];

export function IssueFilters({ value, onChange }: IssueFiltersProps) {
  return (
    <div className="flex items-center gap-4 border-b border-border bg-surface px-4 py-2">
      <FilterGroup label="Status">
        {statuses.map((s) => (
          <Chip
            key={String(s)}
            active={(value.status ?? "all") === s}
            onClick={() => onChange({ ...value, status: s })}
          >
            {s === "all" ? "All" : <StatusBadge status={s as IssueStatus} withLabel />}
          </Chip>
        ))}
      </FilterGroup>
      <FilterGroup label="Priority">
        {priorities.map((p) => (
          <Chip
            key={String(p)}
            active={(value.priority ?? "all") === p}
            onClick={() => onChange({ ...value, priority: p })}
          >
            {p === "all" ? "All" : <PriorityIcon priority={p as IssuePriority} withLabel />}
          </Chip>
        ))}
      </FilterGroup>
    </div>
  );
}

function FilterGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-1">
      <span className="text-[10px] font-semibold uppercase tracking-wider text-muted">
        {label}
      </span>
      <div className="flex items-center gap-1">{children}</div>
    </div>
  );
}

function Chip({
  children,
  active,
  onClick,
}: {
  children: React.ReactNode;
  active?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={clsx(
        "h-6 rounded-full border px-2 text-xs",
        active
          ? "border-accent bg-accent/10 text-accent"
          : "border-border text-muted hover:text-text",
      )}
    >
      {children}
    </button>
  );
}
