import { FileText } from "lucide-react";
import { Dialog } from "~/components/ui/Dialog";
import { useTemplates } from "~/hooks/useTemplates";
import type { IssueTemplate } from "~/api/types";

interface TemplateSelectorProps {
  open: boolean;
  onClose: () => void;
  // pick(null) is "start from scratch"; pick(template) prefills the
  // create dialog with the template's defaults.
  onPick: (template: IssueTemplate | null) => void;
  teamID?: string;
}

// Grid-style picker shown before the IssueCreate dialog. Includes a
// "Blank issue" tile so the user can opt out of templates without
// dismissing the modal.
export function TemplateSelector({ open, onClose, onPick, teamID }: TemplateSelectorProps) {
  const templates = useTemplates(teamID);

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()} title="Pick a template" size="lg">
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 md:grid-cols-3">
        <BlankTile onPick={() => onPick(null)} />
        {(templates.data ?? []).map((t) => (
          <TemplateTile key={t.id} template={t} onPick={() => onPick(t)} />
        ))}
      </div>
      {templates.data && templates.data.length === 0 ? (
        <div className="mt-4 text-center text-xs text-muted">
          No templates yet. The default set will appear once the workspace seeds.
        </div>
      ) : null}
    </Dialog>
  );
}

function TemplateTile({
  template,
  onPick,
}: {
  template: IssueTemplate;
  onPick: () => void;
}) {
  return (
    <button
      onClick={onPick}
      className="flex flex-col items-start gap-1 rounded-md border border-border bg-surface p-3 text-left hover:border-accent"
    >
      <div className="text-xl">{template.icon}</div>
      <div className="text-sm font-medium">{template.name}</div>
      <div className="line-clamp-2 text-[10px] text-muted">{template.description}</div>
    </button>
  );
}

function BlankTile({ onPick }: { onPick: () => void }) {
  return (
    <button
      onClick={onPick}
      className="flex flex-col items-start gap-1 rounded-md border border-dashed border-border bg-bg p-3 text-left hover:border-accent"
    >
      <FileText size={20} className="text-muted" />
      <div className="text-sm font-medium">Blank issue</div>
      <div className="text-[10px] text-muted">Start from scratch.</div>
    </button>
  );
}
