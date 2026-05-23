import * as RM from "@radix-ui/react-dropdown-menu";
import type { ReactNode } from "react";
import clsx from "clsx";

interface DropdownMenuProps {
  trigger: ReactNode;
  children: ReactNode;
  align?: "start" | "center" | "end";
}

export function DropdownMenu({ trigger, children, align = "end" }: DropdownMenuProps) {
  return (
    <RM.Root>
      <RM.Trigger asChild>{trigger}</RM.Trigger>
      <RM.Portal>
        <RM.Content
          align={align}
          sideOffset={4}
          className="z-50 min-w-[12rem] rounded-md border border-border bg-surface p-1 shadow-xl"
        >
          {children}
        </RM.Content>
      </RM.Portal>
    </RM.Root>
  );
}

interface ItemProps {
  children: ReactNode;
  onSelect?: () => void;
  destructive?: boolean;
  disabled?: boolean;
}

export function DropdownItem({ children, onSelect, destructive, disabled }: ItemProps) {
  return (
    <RM.Item
      disabled={disabled}
      onSelect={onSelect}
      className={clsx(
        "flex cursor-default items-center gap-2 rounded px-2 py-1.5 text-sm outline-none",
        destructive ? "text-priority-urgent" : "text-text",
        "data-[highlighted]:bg-bg data-[disabled]:opacity-50",
      )}
    >
      {children}
    </RM.Item>
  );
}

export function DropdownSeparator() {
  return <RM.Separator className="my-1 h-px bg-border" />;
}
