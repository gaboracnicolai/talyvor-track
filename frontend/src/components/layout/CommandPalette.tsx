import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Search, ArrowRight, Sparkles } from "lucide-react";
import { useUIStore } from "~/stores/ui";
import { useWorkspace } from "~/hooks/useWorkspace";
import { issuesApi } from "~/api/issues";
import { Dialog } from "~/components/ui/Dialog";
import { Input } from "~/components/ui/Input";
import { Kbd } from "~/components/ui/Kbd";
import type { Issue } from "~/api/types";
import type { Route } from "~/App";

interface CommandPaletteProps {
  onNavigate: (route: Route) => void;
}

interface Command {
  id: string;
  label: string;
  hint?: string;
  group: string;
  perform: () => void;
}

// Cmd+K palette. Two modes:
//   - empty query: show static navigation + AI suggestions
//   - typed query: full-text search via /issues (results) + a
//     "Semantic search…" CTA that triggers the AI-powered endpoint
// The semantic CTA is what makes Track different — every other
// tracker bolts AI on as a side-panel; here it's the primary
// search affordance.
export function CommandPalette({ onNavigate }: CommandPaletteProps) {
  const open = useUIStore((s) => s.commandPaletteOpen);
  const setOpen = useUIStore((s) => s.setCommandPaletteOpen);
  const focusIssue = useUIStore((s) => s.setSelectedIssueId);
  const { workspaceId } = useWorkspace();
  const [query, setQuery] = useState("");
  const [semantic, setSemantic] = useState(false);

  useEffect(() => {
    if (!open) {
      setQuery("");
      setSemantic(false);
    }
  }, [open]);

  const search = useQuery({
    queryKey: ["palette-search", workspaceId, query, semantic],
    queryFn: () =>
      semantic
        ? issuesApi.semanticSearch(workspaceId, query)
        : issuesApi.search(workspaceId, query),
    enabled: open && query.trim().length >= 2,
  });

  const close = () => setOpen(false);

  const navCommands: Command[] = useMemo(
    () => [
      { id: "go-issues", group: "Go to", label: "Issues", perform: () => onNavigate("issues") },
      { id: "go-board", group: "Go to", label: "Board", perform: () => onNavigate("board") },
      { id: "go-cycles", group: "Go to", label: "Cycles", perform: () => onNavigate("cycles") },
      { id: "go-analytics", group: "Go to", label: "Analytics", perform: () => onNavigate("analytics") },
      { id: "go-settings", group: "Go to", label: "Settings", perform: () => onNavigate("settings") },
    ],
    [onNavigate],
  );

  return (
    <Dialog open={open} onOpenChange={setOpen} size="lg">
      <div className="-m-6">
        <div className="flex items-center gap-2 border-b border-border px-4 py-3">
          <Search size={14} className="text-muted" />
          <Input
            autoFocus
            placeholder={semantic ? "Search semantically…" : "Type a command or search…"}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-7 border-0 bg-transparent px-0 focus:ring-0"
          />
          <Kbd>esc</Kbd>
        </div>

        <div className="max-h-[60vh] overflow-y-auto p-2">
          {query.trim().length === 0 ? (
            <Group title="Go to">
              {navCommands.map((c) => (
                <CommandRow
                  key={c.id}
                  label={c.label}
                  onSelect={() => {
                    c.perform();
                    close();
                  }}
                />
              ))}
            </Group>
          ) : (
            <>
              <Group title={semantic ? "Semantic results" : "Issues"}>
                {search.isLoading ? (
                  <Empty>Searching…</Empty>
                ) : search.data && search.data.length > 0 ? (
                  search.data.map((issue: Issue) => (
                    <CommandRow
                      key={issue.id}
                      label={`${issue.identifier} · ${issue.title}`}
                      onSelect={() => {
                        focusIssue(issue.id);
                        close();
                      }}
                    />
                  ))
                ) : (
                  <Empty>No results</Empty>
                )}
              </Group>
              {!semantic ? (
                <Group title="AI">
                  <CommandRow
                    icon={<Sparkles size={12} className="text-accent" />}
                    label={`Semantic search for "${query}"`}
                    onSelect={() => setSemantic(true)}
                  />
                </Group>
              ) : null}
            </>
          )}
        </div>
      </div>
    </Dialog>
  );
}

function Group({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mb-2">
      <div className="px-2 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted">
        {title}
      </div>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

function CommandRow({
  label,
  icon,
  onSelect,
}: {
  label: string;
  icon?: React.ReactNode;
  onSelect: () => void;
}) {
  return (
    <button
      onClick={onSelect}
      className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-sm text-text hover:bg-bg"
    >
      {icon}
      <span className="flex-1 truncate text-left">{label}</span>
      <ArrowRight size={12} className="text-muted opacity-0 group-hover:opacity-100" />
    </button>
  );
}

function Empty({ children }: { children: React.ReactNode }) {
  return <div className="px-2 py-6 text-center text-xs text-muted">{children}</div>;
}
