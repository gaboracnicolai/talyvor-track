import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { scoringApi } from "~/api/scoring";
import type { ScoringMethod } from "~/api/types";
import { useWorkspace } from "./useWorkspace";
import { useUIStore } from "~/stores/ui";

const errLabel = (err: Error) => err.message;

export function useIssueScore(issueID: string | null) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["score", workspaceId, issueID],
    queryFn: () => scoringApi.getScore(workspaceId, issueID!),
    enabled: !!issueID && !!workspaceId,
    // Score may genuinely not exist (404). Treat the error path as
    // "no score yet" instead of red error UI in the ScorePanel.
    retry: false,
  });
}

export function useSetScore(issueID: string) {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: (body: Parameters<typeof scoringApi.setScore>[2]) =>
      scoringApi.setScore(workspaceId, issueID, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["score", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issue", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["prioritized"] });
    },
    onError: (err: Error) => toast(errLabel(err), "error"),
  });
}

export function useDeleteScore(issueID: string) {
  const { workspaceId } = useWorkspace();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);
  return useMutation({
    mutationFn: () => scoringApi.deleteScore(workspaceId, issueID),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["score", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["issue", workspaceId, issueID] });
      qc.invalidateQueries({ queryKey: ["prioritized"] });
    },
    onError: (err: Error) => toast(errLabel(err), "error"),
  });
}

export function usePrioritizedBacklog(method: ScoringMethod, teamID?: string) {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["prioritized", workspaceId, method, teamID ?? null],
    queryFn: () =>
      scoringApi.prioritized(workspaceId, { method, team_id: teamID, limit: 100 }),
    enabled: !!workspaceId,
  });
}

export function useScoringSummary() {
  const { workspaceId } = useWorkspace();
  return useQuery({
    queryKey: ["scoring-summary", workspaceId],
    queryFn: () => scoringApi.summary(workspaceId),
    enabled: !!workspaceId,
  });
}

// formatScore renders a numeric score with the right rounding per
// method. RICE → 1 decimal, ICE → integer. Shared by the panel + the
// prioritised backlog so the visuals never disagree.
export function formatScore(score: number, method: ScoringMethod): string {
  if (method === "rice") return score.toFixed(1);
  return Math.round(score).toString();
}
