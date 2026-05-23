import { create } from "zustand";

export interface Toast {
  id: number;
  message: string;
  level: "info" | "success" | "warn" | "error";
}

interface UIState {
  commandPaletteOpen: boolean;
  setCommandPaletteOpen: (open: boolean) => void;
  selectedIssueId: string | null;
  setSelectedIssueId: (id: string | null) => void;
  focusedIssueId: string | null;
  setFocusedIssueId: (id: string | null) => void;
  toasts: Toast[];
  toast: (message: string, level?: Toast["level"]) => void;
  dismissToast: (id: number) => void;
}

// Single ephemeral-UI store. Toast messages have monotonically
// increasing IDs so dismissals never collide; auto-dismissal after
// 4 seconds is handled here so callers just fire-and-forget.
let nextToastId = 1;

export const useUIStore = create<UIState>((set) => ({
  commandPaletteOpen: false,
  setCommandPaletteOpen: (open) => set({ commandPaletteOpen: open }),
  selectedIssueId: null,
  setSelectedIssueId: (id) => set({ selectedIssueId: id }),
  focusedIssueId: null,
  setFocusedIssueId: (id) => set({ focusedIssueId: id }),
  toasts: [],
  toast: (message, level = "info") => {
    const id = nextToastId++;
    set((s) => ({ toasts: [...s.toasts, { id, message, level }] }));
    setTimeout(() => {
      set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) }));
    }, 4000);
  },
  dismissToast: (id) =>
    set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));
