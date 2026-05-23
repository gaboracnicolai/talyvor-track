import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Dialog } from "~/components/ui/Dialog";
import { Input } from "~/components/ui/Input";
import { Button } from "~/components/ui/Button";
import { useCreateIssue } from "~/hooks/useIssues";
import { useCustomFields } from "~/hooks/useCustomFields";
import { useWorkspace } from "~/hooks/useWorkspace";
import { teamsApi } from "~/api/teams";
import { useUIStore } from "~/stores/ui";
import { CustomFieldEditor } from "./CustomFieldRow";

interface IssueCreateProps {
  open: boolean;
  onClose: () => void;
}

export function IssueCreate({ open, onClose }: IssueCreateProps) {
  const { workspaceId, memberId } = useWorkspace();
  const teams = useQuery({
    queryKey: ["teams", workspaceId],
    queryFn: () => teamsApi.list(workspaceId),
    enabled: open && !!workspaceId,
  });
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [teamId, setTeamId] = useState<string>("");
  const [fieldValues, setFieldValues] = useState<Record<string, string>>({});
  const toast = useUIStore((s) => s.toast);
  const createMutation = useCreateIssue();

  const chosenTeam = teamId || teams.data?.[0]?.id || "";
  // Render every required custom field in the dialog. Non-required
  // fields stay on the IssueDetail page — the create form should be
  // short by default and only grow for fields the workspace insists
  // can't be omitted.
  const fields = useCustomFields(chosenTeam || undefined);
  const requiredFields = useMemo(
    () => (fields.data ?? []).filter((f) => f.required),
    [fields.data],
  );

  const submit = async () => {
    if (!title.trim()) {
      toast("Title is required", "warn");
      return;
    }
    if (!chosenTeam) {
      toast("No team selected", "error");
      return;
    }
    // Client-side guard: the backend re-validates, but a fast inline
    // message saves a round-trip.
    for (const f of requiredFields) {
      const v = fieldValues[f.id];
      if (!v || !v.trim()) {
        toast(`${f.name} is required`, "warn");
        return;
      }
    }
    try {
      await createMutation.mutateAsync({
        title: title.trim(),
        description: description.trim(),
        team_id: chosenTeam,
        creator_id: memberId,
        priority: 0,
        status: "todo",
        field_values: fieldValues,
      });
      setTitle("");
      setDescription("");
      setFieldValues({});
      onClose();
    } catch {
      // toast is fired by the hook's onError
    }
  };

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()} title="New issue">
      <div className="space-y-3">
        <Input
          autoFocus
          placeholder="Issue title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) submit();
          }}
        />
        <textarea
          placeholder="Description (markdown supported)"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          className="h-32 w-full resize-none rounded-md border border-border bg-bg px-3 py-2 text-sm placeholder:text-muted focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent"
        />
        {requiredFields.length > 0 ? (
          <div className="space-y-2 rounded-md border border-border bg-bg/40 p-3">
            <div className="text-[10px] font-semibold uppercase tracking-wider text-muted">
              Required fields
            </div>
            {requiredFields.map((f) => (
              <div key={f.id} className="flex items-start gap-3">
                <div className="w-28 shrink-0 pt-2 text-xs">
                  {f.name}
                  <span className="ml-1 text-priority-urgent">*</span>
                </div>
                <div className="flex-1">
                  <CustomFieldEditor
                    field={f}
                    value={fieldValues[f.id] ?? ""}
                    onChange={(v) => setFieldValues((prev) => ({ ...prev, [f.id]: v }))}
                  />
                </div>
              </div>
            ))}
          </div>
        ) : null}
        <div className="flex items-center justify-between">
          <select
            value={teamId || teams.data?.[0]?.id || ""}
            onChange={(e) => setTeamId(e.target.value)}
            className="h-8 rounded border border-border bg-bg px-2 text-xs"
          >
            {(teams.data ?? []).map((t) => (
              <option key={t.id} value={t.id}>
                {t.identifier} · {t.name}
              </option>
            ))}
          </select>
          <div className="flex items-center gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button onClick={submit} disabled={createMutation.isPending}>
              {createMutation.isPending ? "Creating…" : "Create issue"}
            </Button>
          </div>
        </div>
      </div>
    </Dialog>
  );
}
