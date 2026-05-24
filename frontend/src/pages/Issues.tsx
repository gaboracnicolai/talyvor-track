import { useEffect, useState } from "react";
import { List, LayoutGrid } from "lucide-react";
import clsx from "clsx";
import { useUIStore } from "~/stores/ui";
import { IssueList } from "~/components/issue/IssueList";
import { IssueDetail } from "~/components/issue/IssueDetail";
import { IssueCreate } from "~/components/issue/IssueCreate";
import { TemplateSelector } from "~/components/issue/TemplateSelector";
import { KanbanBoard } from "./KanbanBoard";
import type { IssueTemplate } from "~/api/types";

interface IssuesPageProps {
  createOpen: boolean;
  setCreateOpen: (open: boolean) => void;
}

type IssuesView = "list" | "board";

// View preference persists across reloads in localStorage so users
// land in the layout they last chose. The key is namespaced so it
// doesn't collide with future per-page preferences.
const viewStorageKey = "track_issues_view";

function readStoredView(): IssuesView {
  const raw = typeof window !== "undefined" ? localStorage.getItem(viewStorageKey) : null;
  return raw === "board" ? "board" : "list";
}

export function IssuesPage({ createOpen, setCreateOpen }: IssuesPageProps) {
  const selectedId = useUIStore((s) => s.selectedIssueId);
  const setSelectedId = useUIStore((s) => s.setSelectedIssueId);
  const [view, setView] = useState<IssuesView>(readStoredView);
  // The template selector intercepts the Header / Kanban "New" button.
  // When the user picks (or skips) the picker flips the create dialog
  // open with whatever template they chose.
  const [pickedTemplate, setPickedTemplate] = useState<IssueTemplate | null>(null);
  const [showCreateAfterPick, setShowCreateAfterPick] = useState(false);

  useEffect(() => {
    localStorage.setItem(viewStorageKey, view);
  }, [view]);

  // Sync external `createOpen` (from Header) to selector flow: when
  // the parent flips it true we *show the selector first*, not the
  // create dialog.
  useEffect(() => {
    if (createOpen) {
      setShowCreateAfterPick(false);
    }
  }, [createOpen]);

  const closeAll = () => {
    setCreateOpen(false);
    setShowCreateAfterPick(false);
    setPickedTemplate(null);
  };

  return (
    <div className="flex h-full flex-col">
      <ViewToggle view={view} onChange={setView} />
      {view === "list" ? (
        <IssueList onOpen={setSelectedId} />
      ) : (
        <KanbanBoard onCreate={() => setCreateOpen(true)} />
      )}
      <IssueDetail issueId={selectedId} onClose={() => setSelectedId(null)} />

      {/* Stage 1: template picker. Stage 2: create dialog with the
          chosen template. Both modals close cleanly via closeAll(). */}
      <TemplateSelector
        open={createOpen && !showCreateAfterPick}
        onClose={closeAll}
        onPick={(template) => {
          setPickedTemplate(template);
          setShowCreateAfterPick(true);
        }}
      />
      <IssueCreate
        open={createOpen && showCreateAfterPick}
        onClose={closeAll}
        initialTemplate={pickedTemplate}
      />
    </div>
  );
}

function ViewToggle({
  view,
  onChange,
}: {
  view: IssuesView;
  onChange: (v: IssuesView) => void;
}) {
  return (
    <div className="flex items-center justify-end gap-1 border-b border-border bg-surface px-3 py-1.5">
      <ToggleButton
        active={view === "list"}
        onClick={() => onChange("list")}
        label="List"
        icon={<List size={12} />}
      />
      <ToggleButton
        active={view === "board"}
        onClick={() => onChange("board")}
        label="Board"
        icon={<LayoutGrid size={12} />}
      />
    </div>
  );
}

function ToggleButton({
  active,
  onClick,
  label,
  icon,
}: {
  active: boolean;
  onClick: () => void;
  label: string;
  icon: React.ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      className={clsx(
        "flex h-7 items-center gap-1 rounded px-2 text-xs",
        active
          ? "bg-bg text-text"
          : "text-muted hover:bg-bg/50 hover:text-text",
      )}
    >
      {icon}
      {label}
    </button>
  );
}
