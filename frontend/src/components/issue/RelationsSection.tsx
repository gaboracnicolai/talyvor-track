import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { X, Plus, AlertCircle } from "lucide-react";
import { issuesApi } from "~/api/issues";
import { useWorkspace } from "~/hooks/useWorkspace";
import {
  useCreateRelation,
  useDeleteRelation,
  useRelations,
} from "~/hooks/useRelations";
import { Button } from "~/components/ui/Button";
import { Input } from "~/components/ui/Input";
import clsx from "clsx";
import type { Issue, RelationType, RelationWithIssue } from "~/api/types";

interface RelationsSectionProps {
  issueID: string;
}

// Linear-style relation panel. Groups relations by type (blocked by /
// blocks / related / duplicates / clones) so the most-load-bearing
// category — blockers — is always on top.
const groupOrder: { type: RelationType; label: string; toneClass: string }[] = [
  { type: "blocked_by", label: "Blocked by", toneClass: "text-priority-urgent" },
  { type: "blocks", label: "Blocks", toneClass: "text-priority-medium" },
  { type: "relates_to", label: "Related to", toneClass: "text-muted" },
  { type: "duplicates", label: "Duplicate of", toneClass: "text-muted" },
  { type: "clones", label: "Clone of", toneClass: "text-muted" },
];

export function RelationsSection({ issueID }: RelationsSectionProps) {
  const { data: relations, isLoading } = useRelations(issueID);
  const remove = useDeleteRelation(issueID);
  const [adding, setAdding] = useState(false);

  const byType: Record<string, RelationWithIssue[]> = {};
  for (const r of relations ?? []) {
    (byType[r.type] ??= []).push(r);
  }

  return (
    <div className="border-t border-border pt-4">
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[10px] font-semibold uppercase tracking-wider text-muted">
          Relations
        </div>
        <button
          onClick={() => setAdding(true)}
          className="flex items-center gap-1 text-xs text-muted hover:text-text"
        >
          <Plus size={12} /> Add
        </button>
      </div>

      {isLoading ? (
        <div className="text-xs text-muted">Loading…</div>
      ) : (relations ?? []).length === 0 ? (
        <div className="text-xs text-muted">No relations yet.</div>
      ) : (
        <div className="space-y-3">
          {groupOrder.map((g) => {
            const items = byType[g.type] ?? [];
            if (items.length === 0) return null;
            return (
              <div key={g.type}>
                <div className={clsx("mb-1 text-[10px] font-medium", g.toneClass)}>
                  {g.label}
                </div>
                <div className="space-y-1">
                  {items.map((r) => (
                    <RelationRow
                      key={r.id}
                      relation={r}
                      onRemove={() => remove.mutate({ targetID: r.issue.id, type: r.type })}
                    />
                  ))}
                </div>
              </div>
            );
          })}
        </div>
      )}

      {adding ? (
        <AddRelationForm
          sourceIssueID={issueID}
          onClose={() => setAdding(false)}
        />
      ) : null}
    </div>
  );
}

function RelationRow({
  relation,
  onRemove,
}: {
  relation: RelationWithIssue;
  onRemove: () => void;
}) {
  const open = relation.issue.status !== "done" && relation.issue.status !== "cancelled";
  return (
    <div className="flex items-center gap-2 rounded-md border border-border bg-bg px-2 py-1.5 text-xs">
      {relation.type === "blocked_by" && open ? (
        <AlertCircle size={12} className="text-priority-urgent" />
      ) : null}
      <span className="font-mono text-muted">{relation.issue.identifier}</span>
      <span className="flex-1 truncate">{relation.issue.title}</span>
      <span className="text-[10px] uppercase tracking-wider text-muted">
        {relation.issue.status.replace("_", " ")}
      </span>
      <button
        onClick={onRemove}
        title="Remove relation"
        className="text-muted hover:text-priority-urgent"
      >
        <X size={12} />
      </button>
    </div>
  );
}

const addableTypes: { value: RelationType; label: string }[] = [
  { value: "blocks", label: "Blocks" },
  { value: "blocked_by", label: "Blocked by" },
  { value: "relates_to", label: "Related to" },
  { value: "duplicates", label: "Duplicate of" },
  { value: "clones", label: "Clone of" },
];

function AddRelationForm({
  sourceIssueID,
  onClose,
}: {
  sourceIssueID: string;
  onClose: () => void;
}) {
  const { workspaceId } = useWorkspace();
  const create = useCreateRelation(sourceIssueID);
  const [query, setQuery] = useState("");
  const [type, setType] = useState<RelationType>("blocks");

  // Reuse the existing /issues/search endpoint — type at least two
  // characters before issuing the request so we don't slam the API
  // on every keystroke.
  const search = useQuery({
    queryKey: ["issue-search", workspaceId, query],
    queryFn: () => issuesApi.search(workspaceId, query),
    enabled: query.trim().length >= 2,
  });

  const submit = (target: Issue) => {
    if (target.id === sourceIssueID) return;
    create.mutate(
      { targetID: target.id, type },
      {
        onSuccess: () => {
          setQuery("");
          onClose();
        },
      },
    );
  };

  return (
    <div className="mt-3 space-y-2 rounded-md border border-border bg-bg/40 p-2">
      <div className="flex items-center gap-2">
        <select
          value={type}
          onChange={(e) => setType(e.target.value as RelationType)}
          className="h-8 rounded border border-border bg-bg px-2 text-xs"
        >
          {addableTypes.map((t) => (
            <option key={t.value} value={t.value}>
              {t.label}
            </option>
          ))}
        </select>
        <Input
          autoFocus
          placeholder="Search issues by title…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <Button variant="ghost" size="sm" onClick={onClose}>
          Cancel
        </Button>
      </div>
      {search.data && search.data.length > 0 ? (
        <div className="max-h-40 overflow-y-auto rounded border border-border bg-surface">
          {search.data
            .filter((i) => i.id !== sourceIssueID)
            .map((i) => (
              <button
                key={i.id}
                onClick={() => submit(i)}
                className="flex w-full items-center gap-2 px-2 py-1.5 text-left text-xs hover:bg-bg"
              >
                <span className="font-mono text-muted">{i.identifier}</span>
                <span className="truncate">{i.title}</span>
              </button>
            ))}
        </div>
      ) : query.trim().length >= 2 && !search.isLoading ? (
        <div className="px-2 py-1 text-xs text-muted">No matches</div>
      ) : null}
    </div>
  );
}
