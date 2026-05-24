import { useState } from "react";
import { useIssue, useUpdateIssue } from "~/hooks/useIssues";
import { useCustomFields, useSetCustomFieldValue } from "~/hooks/useCustomFields";
import { Dialog } from "~/components/ui/Dialog";
import { Input } from "~/components/ui/Input";
import { Button } from "~/components/ui/Button";
import { StatusBadge } from "./StatusBadge";
import { PriorityIcon } from "./PriorityIcon";
import { AICostBadge } from "./AICostBadge";
import { CustomFieldRow } from "./CustomFieldRow";
import { RelationsSection } from "./RelationsSection";
import { DependencyGraph } from "./DependencyGraph";
import { TimeTracker } from "./TimeTracker";
import { ScorePanel } from "./ScorePanel";
import { formatDuration } from "~/hooks/useTimeTracking";
import { formatScore } from "~/hooks/useScoring";
import { Clock, BarChart3 } from "lucide-react";
import { Badge } from "~/components/ui/Badge";
import type { IssueStatus, IssuePriority } from "~/api/types";

interface IssueDetailProps {
  issueId: string | null;
  onClose: () => void;
}

const allStatuses: IssueStatus[] = [
  "backlog",
  "todo",
  "in_progress",
  "in_review",
  "done",
  "cancelled",
];
const allPriorities: IssuePriority[] = [0, 1, 2, 3, 4];

export function IssueDetail({ issueId, onClose }: IssueDetailProps) {
  const { data: issue, isLoading } = useIssue(issueId);
  const updateMutation = useUpdateIssue();
  const setFieldValue = useSetCustomFieldValue();
  const fields = useCustomFields(issue?.team_id);
  const [editingTitle, setEditingTitle] = useState<string | null>(null);
  const [showGraph, setShowGraph] = useState(false);
  const [showScore, setShowScore] = useState(false);

  if (!issueId) return null;

  return (
    <Dialog open={!!issueId} onOpenChange={(open) => !open && onClose()} size="lg">
      {isLoading || !issue ? (
        <div className="py-12 text-center text-sm text-muted">Loading…</div>
      ) : (
        <div className="space-y-4">
          <div className="flex items-center gap-2 text-xs text-muted">
            <span className="font-mono">{issue.identifier}</span>
            <AICostBadge costUSD={issue.ai_cost_usd ?? 0} tokens={issue.ai_tokens ?? 0} />
            {issue.is_blocked ? (
              <span className="inline-flex items-center gap-1 rounded-full border border-priority-urgent/30 bg-priority-urgent/10 px-2 py-0.5 text-[10px] font-medium text-priority-urgent">
                Blocked
              </span>
            ) : null}
            {issue.time_tracked_sec && issue.time_tracked_sec > 0 ? (
              <span
                className="inline-flex items-center gap-1 rounded-full border border-border bg-bg px-2 py-0.5 text-[10px] font-medium text-muted"
                title="Total tracked time"
              >
                <Clock size={10} />
                {formatDuration(issue.time_tracked_sec)}
              </span>
            ) : null}
            {issue.rice_score !== undefined && issue.rice_score !== null ? (
              <button
                onClick={() => setShowScore(true)}
                className="inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-[10px] font-medium text-accent"
                title="RICE score — click to edit"
              >
                <BarChart3 size={10} />
                RICE {formatScore(issue.rice_score, "rice")}
              </button>
            ) : null}
            {issue.ice_score !== undefined && issue.ice_score !== null ? (
              <button
                onClick={() => setShowScore(true)}
                className="inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-[10px] font-medium text-accent"
                title="ICE score — click to edit"
              >
                <BarChart3 size={10} />
                ICE {formatScore(issue.ice_score, "ice")}
              </button>
            ) : null}
          </div>
          {editingTitle === issue.id ? (
            <Input
              autoFocus
              defaultValue={issue.title}
              onBlur={(e) => {
                if (e.target.value !== issue.title) {
                  updateMutation.mutate({ id: issue.id, updates: { title: e.target.value } });
                }
                setEditingTitle(null);
              }}
              onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
            />
          ) : (
            <h2
              className="cursor-text text-lg font-semibold"
              onClick={() => setEditingTitle(issue.id)}
            >
              {issue.title}
            </h2>
          )}
          {issue.description ? (
            <p className="whitespace-pre-wrap text-sm text-muted">{issue.description}</p>
          ) : null}

          <div className="grid grid-cols-2 gap-4 border-t border-border pt-4 text-sm">
            <Field label="Status">
              <div className="flex flex-wrap gap-1">
                {allStatuses.map((s) => (
                  <button
                    key={s}
                    onClick={() => updateMutation.mutate({ id: issue.id, updates: { status: s } })}
                    className={
                      s === issue.status
                        ? "rounded border border-accent px-2 py-1"
                        : "rounded border border-border px-2 py-1 hover:border-border/80"
                    }
                  >
                    <StatusBadge status={s} withLabel />
                  </button>
                ))}
              </div>
            </Field>
            <Field label="Priority">
              <div className="flex flex-wrap gap-1">
                {allPriorities.map((p) => (
                  <button
                    key={p}
                    onClick={() => updateMutation.mutate({ id: issue.id, updates: { priority: p } })}
                    className={
                      p === issue.priority
                        ? "rounded border border-accent px-2 py-1"
                        : "rounded border border-border px-2 py-1 hover:border-border/80"
                    }
                  >
                    <PriorityIcon priority={p} withLabel />
                  </button>
                ))}
              </div>
            </Field>
            <Field label="Labels">
              {(issue.labels ?? []).length > 0 ? (
                <div className="flex flex-wrap gap-1">
                  {issue.labels!.map((l) => (
                    <Badge key={l}>{l}</Badge>
                  ))}
                </div>
              ) : (
                <span className="text-xs text-muted">—</span>
              )}
            </Field>
            <Field label="Assignee">
              <span className="text-xs text-muted">{issue.assignee_id ?? "Unassigned"}</span>
            </Field>
          </div>

          {fields.data && fields.data.length > 0 ? (
            <div className="border-t border-border pt-4">
              <div className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted">
                Custom fields
              </div>
              <div className="space-y-1">
                {fields.data.map((f) => (
                  <CustomFieldRow
                    key={f.id}
                    field={f}
                    value={issue.field_values?.[f.id]}
                    onChange={(v) =>
                      setFieldValue.mutate({ issueID: issue.id, fieldID: f.id, value: v })
                    }
                  />
                ))}
              </div>
            </div>
          ) : null}

          <RelationsSection issueID={issue.id} />

          <div className="border-t border-border pt-4">
            <div className="mb-2 flex items-center justify-between">
              <div className="flex items-center gap-2 text-[10px] font-semibold uppercase tracking-wider text-muted">
                <BarChart3 size={12} />
                Prioritisation
              </div>
              {!showScore ? (
                <button
                  onClick={() => setShowScore(true)}
                  className="text-xs text-muted hover:text-text"
                >
                  Set priority score
                </button>
              ) : null}
            </div>
            {showScore ? (
              <ScorePanel issueID={issue.id} onClose={() => setShowScore(false)} />
            ) : null}
          </div>

          <TimeTracker issueID={issue.id} />

          <div className="flex justify-between gap-2 border-t border-border pt-4">
            <Button variant="ghost" onClick={() => setShowGraph((v) => !v)}>
              {showGraph ? "Hide dependency graph" : "Show dependency graph"}
            </Button>
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
          </div>

          {showGraph ? <DependencyGraph issueID={issue.id} /> : null}
        </div>
      )}
    </Dialog>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted">
        {label}
      </div>
      {children}
    </div>
  );
}
