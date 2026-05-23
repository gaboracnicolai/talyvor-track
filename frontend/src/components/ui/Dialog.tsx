import * as RD from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ReactNode } from "react";

interface DialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title?: string;
  children: ReactNode;
  size?: "sm" | "md" | "lg";
}

const sizeClass = {
  sm: "max-w-md",
  md: "max-w-lg",
  lg: "max-w-2xl",
};

export function Dialog({ open, onOpenChange, title, children, size = "md" }: DialogProps) {
  return (
    <RD.Root open={open} onOpenChange={onOpenChange}>
      <RD.Portal>
        <RD.Overlay className="fixed inset-0 z-40 bg-black/60 backdrop-blur-sm" />
        <RD.Content
          className={`fixed left-1/2 top-1/2 z-50 w-full -translate-x-1/2 -translate-y-1/2 ${sizeClass[size]} rounded-lg border border-border bg-surface p-6 shadow-2xl focus:outline-none`}
        >
          {title ? (
            <div className="mb-4 flex items-center justify-between">
              <RD.Title className="text-base font-semibold text-text">{title}</RD.Title>
              <RD.Close className="text-muted hover:text-text">
                <X size={16} />
              </RD.Close>
            </div>
          ) : null}
          {children}
        </RD.Content>
      </RD.Portal>
    </RD.Root>
  );
}
