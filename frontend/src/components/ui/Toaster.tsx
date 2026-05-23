import { X } from "lucide-react";
import { useUIStore } from "~/stores/ui";
import clsx from "clsx";

const levelClass = {
  info: "border-border bg-surface",
  success: "border-status-done bg-surface",
  warn: "border-priority-medium bg-surface",
  error: "border-priority-urgent bg-surface",
};

export function Toaster() {
  const toasts = useUIStore((s) => s.toasts);
  const dismiss = useUIStore((s) => s.dismissToast);
  return (
    <div className="pointer-events-none fixed bottom-4 right-4 z-50 flex w-80 flex-col gap-2">
      {toasts.map((t) => (
        <div
          key={t.id}
          className={clsx(
            "pointer-events-auto flex items-start gap-2 rounded-md border p-3 text-sm shadow-xl",
            levelClass[t.level],
          )}
        >
          <div className="flex-1 text-text">{t.message}</div>
          <button onClick={() => dismiss(t.id)} className="text-muted hover:text-text">
            <X size={14} />
          </button>
        </div>
      ))}
    </div>
  );
}
