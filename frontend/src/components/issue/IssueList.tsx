import { useEffect, useMemo, useRef, useState } from "react";
import { useIssues, useUpdateIssue } from "~/hooks/useIssues";
import { useKeyboard } from "~/hooks/useKeyboard";
import { useUIStore } from "~/stores/ui";
import { useWorkspace } from "~/hooks/useWorkspace";
import { IssueRow } from "./IssueRow";
import { IssueFilters, type IssueFilterValue } from "./IssueFilters";
import type { Issue } from "~/api/types";

const orderedStatuses: Issue["status"][] = [
  "backlog",
  "todo",
  "in_progress",
  "in_review",
  "done",
  "cancelled",
];

// Linear-style flat list with J/K navigation, Space to open, E to
// edit, 1–4 to set priority. Filtering is client-side (fast for
// typical workspaces); switch to server filtering if a workspace
// breaks past ~5k issues.
export function IssueList({ onOpen }: { onOpen: (id: string) => void }) {
  const { teamId } = useWorkspace();
  const [filters, setFilters] = useState<IssueFilterValue>({ status: "all", priority: "all" });
  const { data, isLoading, error } = useIssues(teamId ? { team_id: teamId } : {});
  const focusedId = useUIStore((s) => s.focusedIssueId);
  const setFocused = useUIStore((s) => s.setFocusedIssueId);
  const selectedId = useUIStore((s) => s.selectedIssueId);
  const updateMutation = useUpdateIssue();
  const containerRef = useRef<HTMLDivElement>(null);

  const filtered = useMemo(() => {
    if (!data) return [];
    return data.filter((i) => {
      if (filters.status && filters.status !== "all" && i.status !== filters.status) return false;
      if (filters.priority && filters.priority !== "all" && i.priority !== filters.priority) return false;
      return true;
    });
  }, [data, filters]);

  const sorted = useMemo(() => {
    return [...filtered].sort((a, b) => {
      const sa = orderedStatuses.indexOf(a.status);
      const sb = orderedStatuses.indexOf(b.status);
      if (sa !== sb) return sa - sb;
      if (a.priority !== b.priority) return a.priority - b.priority;
      return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime();
    });
  }, [filtered]);

  useEffect(() => {
    if (!focusedId && sorted.length > 0) {
      setFocused(sorted[0].id);
    }
  }, [focusedId, sorted, setFocused]);

  const focusedIndex = sorted.findIndex((i) => i.id === focusedId);

  useKeyboard(
    {
      j: () => {
        const next = sorted[Math.min(focusedIndex + 1, sorted.length - 1)];
        if (next) setFocused(next.id);
      },
      k: () => {
        const prev = sorted[Math.max(focusedIndex - 1, 0)];
        if (prev) setFocused(prev.id);
      },
      arrowdown: () => {
        const next = sorted[Math.min(focusedIndex + 1, sorted.length - 1)];
        if (next) setFocused(next.id);
      },
      arrowup: () => {
        const prev = sorted[Math.max(focusedIndex - 1, 0)];
        if (prev) setFocused(prev.id);
      },
      enter: () => {
        if (focusedId) onOpen(focusedId);
      },
      " ": (e) => {
        e.preventDefault();
        if (focusedId) onOpen(focusedId);
      },
      "1": () => {
        if (focusedId) updateMutation.mutate({ id: focusedId, updates: { priority: 1 } });
      },
      "2": () => {
        if (focusedId) updateMutation.mutate({ id: focusedId, updates: { priority: 2 } });
      },
      "3": () => {
        if (focusedId) updateMutation.mutate({ id: focusedId, updates: { priority: 3 } });
      },
      "4": () => {
        if (focusedId) updateMutation.mutate({ id: focusedId, updates: { priority: 4 } });
      },
    },
    [sorted, focusedId, focusedIndex],
  );

  // Keep the focused row scrolled into view when J/K moves past the
  // viewport. queryselector beats refs here because rows aren't keyed
  // by index — issues can shift order when state changes.
  useEffect(() => {
    if (!focusedId || !containerRef.current) return;
    const el = containerRef.current.querySelector<HTMLElement>(`[data-issue-id="${focusedId}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [focusedId]);

  if (error) {
    return (
      <div className="p-8 text-center text-sm text-priority-urgent">
        Failed to load: {(error as Error).message}
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <IssueFilters value={filters} onChange={setFilters} />
      <div ref={containerRef} className="flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="p-8 text-center text-sm text-muted">Loading issues…</div>
        ) : sorted.length === 0 ? (
          <Empty />
        ) : (
          sorted.map((issue) => (
            <IssueRow
              key={issue.id}
              issue={issue}
              focused={issue.id === focusedId}
              selected={issue.id === selectedId}
              onClick={() => onOpen(issue.id)}
            />
          ))
        )}
      </div>
    </div>
  );
}

function Empty() {
  return (
    <div className="flex h-64 flex-col items-center justify-center gap-1 text-muted">
      <div className="text-sm font-medium">No issues match your filters.</div>
      <div className="text-xs">Try clearing them or pressing N to create one.</div>
    </div>
  );
}
