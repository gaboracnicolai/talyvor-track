import type { ReactNode } from "react";
import clsx from "clsx";

interface BadgeProps {
  children: ReactNode;
  color?: string; // hex code; falls back to muted text
  className?: string;
}

// Generic pill component. Colour is passed as a CSS variable so any
// hex value works without growing the variant table — used for both
// status pills (which read team-configured colors) and label pills.
export function Badge({ children, color, className }: BadgeProps) {
  const style = color
    ? { color, borderColor: color + "40", backgroundColor: color + "1a" }
    : undefined;
  return (
    <span
      style={style}
      className={clsx(
        "inline-flex items-center gap-1 rounded-full border border-border",
        "bg-surface px-2 py-0.5 text-xs font-medium text-muted",
        className,
      )}
    >
      {children}
    </span>
  );
}
