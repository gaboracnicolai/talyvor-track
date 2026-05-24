import clsx from "clsx";
import type { FeaturePostStatus } from "~/api/types";

const config: Record<
  FeaturePostStatus,
  { label: string; className: string }
> = {
  open: { label: "Open", className: "bg-bg text-muted border-border" },
  planned: { label: "Planned", className: "bg-accent/10 text-accent border-accent/30" },
  in_progress: {
    label: "In progress",
    className: "bg-status-in_progress/10 text-status-in_progress border-status-in_progress/30",
  },
  completed: {
    label: "Completed",
    className: "bg-status-done/10 text-status-done border-status-done/30",
  },
  declined: {
    label: "Declined",
    className: "bg-priority-urgent/10 text-priority-urgent border-priority-urgent/30",
  },
};

export function BoardStatusBadge({ status }: { status: FeaturePostStatus }) {
  const c = config[status] ?? config.open;
  return (
    <span
      className={clsx(
        "inline-flex h-5 items-center rounded-full border px-2 text-[10px] font-medium",
        c.className,
      )}
    >
      {c.label}
    </span>
  );
}
