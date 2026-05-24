import { AlertCircle, ArrowRight, Link2 } from "lucide-react";
import { Tooltip } from "~/components/ui/Tooltip";
import { useRelations } from "~/hooks/useRelations";
import type { RelationType } from "~/api/types";

interface RelationSummaryProps {
  issueID: string;
}

// Inline chip cluster shown on the issue row. Only renders when at
// least one relation exists — silent on the common no-relations
// case so the list stays uncluttered. The full relations panel
// lives in IssueDetail; this is a sniff test that scrolls fast.
export function RelationSummary({ issueID }: RelationSummaryProps) {
  const { data } = useRelations(issueID);
  if (!data || data.length === 0) return null;

  const groups: Record<string, string[]> = {};
  for (const r of data) {
    (groups[r.type] ??= []).push(r.issue.identifier);
  }

  const blockedBy = groups["blocked_by"] ?? [];
  const blocking = groups["blocks"] ?? [];
  const related = [
    ...(groups["relates_to"] ?? []),
    ...(groups["duplicates"] ?? []),
    ...(groups["clones"] ?? []),
  ];

  return (
    <span className="inline-flex items-center gap-1">
      {blockedBy.length > 0 ? (
        <Chip
          count={blockedBy.length}
          label="Blocked by"
          ids={blockedBy}
          icon={<AlertCircle size={10} />}
          tone="urgent"
          type="blocked_by"
        />
      ) : null}
      {blocking.length > 0 ? (
        <Chip
          count={blocking.length}
          label="Blocking"
          ids={blocking}
          icon={<ArrowRight size={10} />}
          tone="medium"
          type="blocks"
        />
      ) : null}
      {related.length > 0 ? (
        <Chip
          count={related.length}
          label="Related"
          ids={related}
          icon={<Link2 size={10} />}
          tone="muted"
          type="relates_to"
        />
      ) : null}
    </span>
  );
}

function Chip({
  count,
  label,
  ids,
  icon,
  tone,
}: {
  count: number;
  label: string;
  ids: string[];
  icon: React.ReactNode;
  tone: "urgent" | "medium" | "muted";
  type: RelationType;
}) {
  const toneClass =
    tone === "urgent"
      ? "border-priority-urgent/40 text-priority-urgent"
      : tone === "medium"
        ? "border-priority-medium/40 text-priority-medium"
        : "border-border text-muted";
  return (
    <Tooltip content={`${label}: ${ids.join(", ")}`}>
      <span
        className={`inline-flex h-5 items-center gap-1 rounded-full border bg-bg px-1.5 text-[10px] ${toneClass}`}
      >
        {icon}
        {count}
      </span>
    </Tooltip>
  );
}
