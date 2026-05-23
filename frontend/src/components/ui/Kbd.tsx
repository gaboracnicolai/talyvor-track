import type { ReactNode } from "react";
import clsx from "clsx";

interface KbdProps {
  children: ReactNode;
  className?: string;
}

// Inline keyboard-key indicator. Uses IBM Plex Mono so glyphs sit on
// a consistent baseline across themes; modifier symbols (⌘, ⌃) are
// passed in by callers verbatim.
export function Kbd({ children, className }: KbdProps) {
  return (
    <kbd
      className={clsx(
        "inline-flex h-5 min-w-[1.25rem] items-center justify-center rounded",
        "border border-border bg-bg px-1.5 font-mono text-[10px] font-medium text-muted",
        className,
      )}
    >
      {children}
    </kbd>
  );
}
