import { useEffect, useMemo, useRef, useState } from "react";
import { useIssues, useBulkUpdateIssues } from "~/hooks/useIssues";
import { useKeyboard } from "~/hooks/useKeyboard";
import { useUIStore } from "~/stores/ui";
import { useWorkspace } from "~/hooks/useWorkspace";
import { KanbanColumn } from "~/components/kanban/KanbanColumn";
import { KanbanCard } from "~/components/kanban/KanbanCard";
import { IssueDetail } from "~/components/issue/IssueDetail";
import { IssueFilters, type IssueFilterValue } from "~/components/issue/IssueFilters";
import type { BulkUpdateItem } from "~/api/issues";
import type { Issue, IssueStatus } from "~/api/types";

interface KanbanBoardProps {
  onCreate?: () => void;
}

// Column ordering matches the issue lifecycle. "cancelled" is
// rendered last and almost always empty in active work; it's there
// so cards can move into it without an extra dropdown.
const columns: { status: IssueStatus; title: string }[] = [
  { status: "backlog", title: "Backlog" },
  { status: "todo", title: "Todo" },
  { status: "in_progress", title: "In Progress" },
  { status: "in_review", title: "In Review" },
  { status: "done", title: "Done" },
  { status: "cancelled", title: "Cancelled" },
];

// dragThreshold (in px) prevents a click from becoming a drag. Five
// pixels matches platform-native click vs drag heuristics.
const dragThreshold = 5;

interface DragState {
  issue: Issue;
  startX: number;
  startY: number;
  // The "ghost" element follows the pointer; ref so we can move it
  // imperatively without re-rendering on every pointer-move.
  ghost: HTMLDivElement | null;
  isDragging: boolean;
}

// KanbanBoard implements a full drag-and-drop kanban using pointer
// events so the same code path works for mouse + touch. The drop
// target is discovered via document.elementFromPoint at pointer-up
// — no per-column drop-zone listeners needed.
//
// State machine on each card press:
//   1. pointerdown captures the start position + issue
//   2. pointermove (above dragThreshold) lifts the card and renders
//      a floating ghost that tracks the pointer
//   3. pointerup detects the drop column via elementFromPoint and
//      issues the bulk-update. If the pointer moved less than the
//      threshold, treat the gesture as a click and open the issue.
export function KanbanBoard({ onCreate }: KanbanBoardProps) {
  const { teamId } = useWorkspace();
  const [filters, setFilters] = useState<IssueFilterValue>({
    status: "all",
    priority: "all",
  });
  const { data, isLoading } = useIssues(teamId ? { team_id: teamId } : {});
  const bulk = useBulkUpdateIssues();
  const selectedId = useUIStore((s) => s.selectedIssueId);
  const setSelectedId = useUIStore((s) => s.setSelectedIssueId);
  const focusedId = useUIStore((s) => s.focusedIssueId);
  const setFocused = useUIStore((s) => s.setFocusedIssueId);

  // Group + sort issues per column. sort_order ascending matches the
  // server's source of truth.
  const grouped = useMemo(() => {
    const empty = (): Issue[] => [];
    const out: Record<IssueStatus, Issue[]> = {
      backlog: empty(),
      todo: empty(),
      in_progress: empty(),
      in_review: empty(),
      done: empty(),
      cancelled: empty(),
    };
    for (const i of data ?? []) {
      if (
        filters.priority &&
        filters.priority !== "all" &&
        i.priority !== filters.priority
      ) {
        continue;
      }
      out[i.status]?.push(i);
    }
    for (const k of Object.keys(out) as IssueStatus[]) {
      out[k].sort((a, b) => a.sort_order - b.sort_order);
    }
    return out;
  }, [data, filters]);

  // ─── pointer-event DnD ─────────────────────────────────
  const dragRef = useRef<DragState | null>(null);
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<IssueStatus | null>(null);

  const onCardPointerDown = (
    issue: Issue,
    e: React.PointerEvent<HTMLDivElement>,
  ) => {
    if (e.button !== 0 && e.pointerType === "mouse") return; // ignore right/middle clicks
    dragRef.current = {
      issue,
      startX: e.clientX,
      startY: e.clientY,
      ghost: null,
      isDragging: false,
    };
    // Capture so pointermove + pointerup land on this element even if
    // the pointer wanders off the card.
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  };

  // Single document-level listener for pointermove + pointerup so we
  // don't need per-card subscribers. Mounted/unmounted with the
  // component lifecycle.
  useEffect(() => {
    const onMove = (e: PointerEvent) => {
      const drag = dragRef.current;
      if (!drag) return;
      const dx = e.clientX - drag.startX;
      const dy = e.clientY - drag.startY;
      if (!drag.isDragging) {
        if (Math.hypot(dx, dy) < dragThreshold) return;
        drag.isDragging = true;
        setDraggingId(drag.issue.id);
        // Lazily build the floating ghost the first time we cross the
        // threshold so a plain click doesn't pay for it.
        const ghost = document.createElement("div");
        ghost.className =
          "pointer-events-none fixed z-50 rounded-md border border-accent bg-surface px-3 py-2 text-xs shadow-2xl";
        ghost.style.maxWidth = "240px";
        ghost.textContent = `${drag.issue.identifier} · ${drag.issue.title}`;
        document.body.appendChild(ghost);
        drag.ghost = ghost;
      }
      if (drag.ghost) {
        drag.ghost.style.left = `${e.clientX + 8}px`;
        drag.ghost.style.top = `${e.clientY + 8}px`;
      }
      // Highlight the column the pointer is currently over.
      const el = document.elementFromPoint(e.clientX, e.clientY);
      const colEl = el?.closest("[data-kanban-column]") as HTMLElement | null;
      const status = colEl?.dataset.status as IssueStatus | undefined;
      setDropTarget(status ?? null);
    };

    const onUp = (e: PointerEvent) => {
      const drag = dragRef.current;
      if (!drag) return;
      // Clean up the ghost regardless of outcome.
      drag.ghost?.remove();
      if (!drag.isDragging) {
        // Below-threshold gesture: treat as a click.
        dragRef.current = null;
        return;
      }
      const el = document.elementFromPoint(e.clientX, e.clientY);
      const colEl = el?.closest("[data-kanban-column]") as HTMLElement | null;
      const status = colEl?.dataset.status as IssueStatus | undefined;
      if (status) {
        const updates = computeBulkUpdate({
          dragged: drag.issue,
          newStatus: status,
          dropY: e.clientY,
          grouped,
        });
        if (updates.length > 0) bulk.mutate(updates);
      }
      dragRef.current = null;
      setDraggingId(null);
      setDropTarget(null);
    };

    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onUp);
    return () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onUp);
    };
  }, [grouped, bulk]);

  // Flat list for keyboard navigation across all columns.
  const ordered = useMemo(
    () => columns.flatMap((c) => grouped[c.status]),
    [grouped],
  );
  const focusedIndex = ordered.findIndex((i) => i.id === focusedId);

  useKeyboard(
    {
      j: () => setFocused(ordered[Math.min(focusedIndex + 1, ordered.length - 1)]?.id ?? null),
      k: () => setFocused(ordered[Math.max(focusedIndex - 1, 0)]?.id ?? null),
      arrowdown: () => setFocused(ordered[Math.min(focusedIndex + 1, ordered.length - 1)]?.id ?? null),
      arrowup: () => setFocused(ordered[Math.max(focusedIndex - 1, 0)]?.id ?? null),
      enter: () => focusedId && setSelectedId(focusedId),
      c: () => onCreate?.(),
    },
    [ordered, focusedIndex, focusedId, onCreate],
  );

  return (
    <div className="flex h-full flex-col">
      <IssueFilters value={filters} onChange={setFilters} />
      <div className="flex flex-1 gap-3 overflow-x-auto p-4">
        {isLoading ? (
          <div className="text-xs text-muted">Loading…</div>
        ) : (
          columns.map((col) => (
            <KanbanColumn
              key={col.status}
              status={col.status}
              title={col.title}
              issues={grouped[col.status]}
              focusedId={focusedId}
              draggingId={draggingId}
              isDropTarget={dropTarget === col.status}
              onCardClick={(id) => setSelectedId(id)}
              onCardPointerDown={onCardPointerDown}
              onCreate={onCreate}
            />
          ))
        )}
      </div>
      <IssueDetail issueId={selectedId} onClose={() => setSelectedId(null)} />
    </div>
  );
}

// computeBulkUpdate produces the list of issues that need their
// sort_order (and maybe status) patched after a drop. We use a gap
// algorithm to avoid renumbering every card in a column on every
// move:
//   - Empty column → sort_order = 1
//   - Top of column → first.sort_order - 1
//   - Bottom of column → last.sort_order + 1
//   - Between i-1 and i → (prev + next) / 2
//
// We don't touch unrelated cards' sort_orders; only the dragged card
// changes. That keeps the bulk-update payload small (one row) on the
// common case.
export function computeBulkUpdate({
  dragged,
  newStatus,
  dropY,
  grouped,
}: {
  dragged: Issue;
  newStatus: IssueStatus;
  dropY: number;
  grouped: Record<IssueStatus, Issue[]>;
}): BulkUpdateItem[] {
  // Exclude the dragged card from the target column when computing
  // its new position — otherwise we'd average against ourselves.
  const targetCards = grouped[newStatus].filter((i) => i.id !== dragged.id);

  // dropIndex is determined by walking cards in the target column
  // and finding the first whose midpoint is below the drop Y. Using
  // getBoundingClientRect against live DOM is robust to virtual
  // scroll / variable card heights.
  let dropIndex = targetCards.length; // default to bottom
  for (let i = 0; i < targetCards.length; i++) {
    const el = document.querySelector(
      `[data-issue-id="${targetCards[i].id}"]`,
    ) as HTMLElement | null;
    if (!el) continue;
    const rect = el.getBoundingClientRect();
    if (dropY < rect.top + rect.height / 2) {
      dropIndex = i;
      break;
    }
  }

  let sortOrder: number;
  if (targetCards.length === 0) {
    sortOrder = 1;
  } else if (dropIndex === 0) {
    sortOrder = targetCards[0].sort_order - 1;
  } else if (dropIndex === targetCards.length) {
    sortOrder = targetCards[targetCards.length - 1].sort_order + 1;
  } else {
    sortOrder =
      (targetCards[dropIndex - 1].sort_order +
        targetCards[dropIndex].sort_order) /
      2;
  }

  // No-op detection: same column, same neighbours → no patch needed.
  // We still emit the update if the status changed even when the
  // sort_order happens to round-trip.
  const sameStatus = dragged.status === newStatus;
  if (sameStatus && Math.abs(sortOrder - dragged.sort_order) < 1e-9) {
    return [];
  }

  const update: BulkUpdateItem = { id: dragged.id, sort_order: sortOrder };
  if (!sameStatus) update.status = newStatus;
  return [update];
}

// Allow the page to embed a focused card preview in unit tests / dev
// without importing the column. Re-exported for completeness — not
// part of the runtime API surface.
export { KanbanCard };
