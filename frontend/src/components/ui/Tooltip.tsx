import * as RT from "@radix-ui/react-tooltip";
import type { ReactNode } from "react";

interface TooltipProps {
  content: ReactNode;
  children: ReactNode;
  side?: "top" | "right" | "bottom" | "left";
}

// Radix tooltip thin wrapper. Wrap children that need a tooltip;
// content is plain text or arbitrary nodes. Provider sits at the
// component root rather than the app root so tooltips in isolated
// trees (e.g. test fixtures) still work.
export function Tooltip({ content, children, side = "top" }: TooltipProps) {
  return (
    <RT.Provider delayDuration={200}>
      <RT.Root>
        <RT.Trigger asChild>{children}</RT.Trigger>
        <RT.Portal>
          <RT.Content
            side={side}
            sideOffset={4}
            className="z-50 max-w-xs rounded-md border border-border bg-surface px-2 py-1 text-xs text-text shadow-lg"
          >
            {content}
            <RT.Arrow className="fill-surface" />
          </RT.Content>
        </RT.Portal>
      </RT.Root>
    </RT.Provider>
  );
}
