import { AlertTriangle, ArrowUp, Minus, ArrowDown } from "lucide-react";
import type { IssuePriority } from "~/api/types";
import clsx from "clsx";

const config: Record<
  IssuePriority,
  { label: string; Icon: typeof AlertTriangle | null; color: string }
> = {
  0: { label: "No priority", Icon: null, color: "text-muted" },
  1: { label: "Urgent", Icon: AlertTriangle, color: "text-priority-urgent" },
  2: { label: "High", Icon: ArrowUp, color: "text-priority-high" },
  3: { label: "Medium", Icon: Minus, color: "text-priority-medium" },
  4: { label: "Low", Icon: ArrowDown, color: "text-priority-low" },
};

interface PriorityIconProps {
  priority: IssuePriority;
  withLabel?: boolean;
  className?: string;
}

export function PriorityIcon({ priority, withLabel, className }: PriorityIconProps) {
  const c = config[priority] ?? config[0];
  const Icon = c.Icon;
  if (!Icon) {
    return withLabel ? <span className={clsx("text-xs text-muted", className)}>—</span> : null;
  }
  return (
    <span className={clsx("inline-flex items-center gap-1", c.color, className)}>
      <Icon size={14} strokeWidth={2} />
      {withLabel ? <span className="text-xs">{c.label}</span> : null}
    </span>
  );
}
