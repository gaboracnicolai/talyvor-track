import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timeApi } from "~/api/timetracking";
import { useWorkspace } from "./useWorkspace";
import { useUIStore } from "~/stores/ui";

// useTimer polls the running-timer endpoint every 5 seconds AND
// keeps a local 1-second tick so the displayed elapsed counter
// updates smoothly without paying for a network round-trip every
// second. The local tick is reset whenever a fresh server snapshot
// arrives.
export function useTimer() {
  const { workspaceId, memberId } = useWorkspace();

  const query = useQuery({
    queryKey: ["timer", workspaceId, memberId],
    queryFn: () => timeApi.getTimer(workspaceId, memberId),
    enabled: !!workspaceId && !!memberId,
    refetchInterval: 5_000,
    refetchOnWindowFocus: true,
  });

  // localTick is the wall-clock-offset between the last server sync
  // and "now". The visible elapsed is server_elapsed + localTick.
  const [localTick, setLocalTick] = useState(0);
  useEffect(() => {
    if (!query.data?.running) return;
    setLocalTick(0);
    const t = setInterval(() => setLocalTick((s) => s + 1), 1000);
    return () => clearInterval(t);
  }, [query.data?.running, query.data?.started_at]);

  const elapsed = query.data?.running
    ? (query.data.elapsed_sec ?? 0) + localTick
    : 0;

  return { ...query, elapsed };
}

export function useStartTimer() {
  const { workspaceId, memberId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: ({ issueID, description = "" }: { issueID: string; description?: string }) =>
      timeApi.startTimer(workspaceId, memberId, issueID, description),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["timer"] });
      qc.invalidateQueries({ queryKey: ["time-entries"] });
      qc.invalidateQueries({ queryKey: ["issue"] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useStopTimer() {
  const { workspaceId, memberId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: () => timeApi.stopTimer(workspaceId, memberId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["timer"] });
      qc.invalidateQueries({ queryKey: ["time-entries"] });
      qc.invalidateQueries({ queryKey: ["issue"] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useIssueTimeEntries(issueID: string | null) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["time-entries", workspaceId, issueID],
    queryFn: () => timeApi.listForIssue(workspaceId, issueID!),
    enabled: !!issueID && !!workspaceId,
  });
}

export function useLogTime(issueID: string) {
  const { workspaceId, memberId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (vars: {
      description: string;
      started_at: string;
      stopped_at: string;
      billable: boolean;
    }) =>
      timeApi.logTime(workspaceId, {
        issue_id: issueID,
        member_id: memberId,
        ...vars,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["time-entries", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issue", workspaceId, issueID] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useDeleteTimeEntry(issueID: string) {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (id: string) => timeApi.remove(workspaceId, id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["time-entries", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issue", workspaceId, issueID] });
    },
    onError: (err: Error) => toast(err.message, "error"),
  });
}

export function useWorkspaceTimeSummary(sinceISO: string) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["time-summary", workspaceId, sinceISO],
    queryFn: () => timeApi.workspaceSummary(workspaceId, sinceISO),
    enabled: !!workspaceId,
  });
}

// formatDuration renders seconds as "Xh Ym" or "Ym" for sub-hour
// values. Exported so the report page + tracker can both use it.
export function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  const rem = m % 60;
  return rem === 0 ? `${h}h` : `${h}h ${rem}m`;
}
