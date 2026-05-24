import { useQuery } from "@tanstack/react-query";
import {
  Inbox,
  Calendar,
  Map,
  Clock,
  BarChart3,
  Settings,
  Plus,
} from "lucide-react";
import clsx from "clsx";
import { teamsApi } from "~/api/teams";
import { projectsApi } from "~/api/projects";
import { useWorkspace } from "~/hooks/useWorkspace";
import type { Route } from "~/App";
import { Avatar } from "~/components/ui/Avatar";

interface SidebarProps {
  route: Route;
  onNavigate: (route: Route) => void;
}

// List/Board toggle lives inside the Issues page header now, so the
// sidebar only carries top-level destinations.
const sections: { route: Route; label: string; Icon: typeof Inbox }[] = [
  { route: "issues", label: "Issues", Icon: Inbox },
  { route: "cycles", label: "Cycles", Icon: Calendar },
  { route: "roadmap", label: "Roadmap", Icon: Map },
  { route: "time", label: "Time", Icon: Clock },
  { route: "analytics", label: "Analytics", Icon: BarChart3 },
  { route: "settings", label: "Settings", Icon: Settings },
];

export function Sidebar({ route, onNavigate }: SidebarProps) {
  const { workspaceId } = useWorkspace();
  const teams = useQuery({
    queryKey: ["teams", workspaceId],
    queryFn: () => teamsApi.list(workspaceId),
    enabled: !!workspaceId,
  });
  const projects = useQuery({
    queryKey: ["projects", workspaceId],
    queryFn: () => projectsApi.list(workspaceId),
    enabled: !!workspaceId,
  });

  return (
    <aside className="hidden w-56 shrink-0 flex-col border-r border-border bg-surface md:flex">
      <div className="flex h-12 items-center gap-2 border-b border-border px-4">
        <div className="flex h-6 w-6 items-center justify-center rounded bg-accent text-bg">
          <span className="font-mono text-xs font-bold">T</span>
        </div>
        <span className="text-sm font-semibold">Talyvor Track</span>
      </div>

      <nav className="flex-1 overflow-y-auto p-2">
        <div className="mb-4 space-y-0.5">
          {sections.map(({ route: r, label, Icon }) => (
            <button
              key={r}
              onClick={() => onNavigate(r)}
              className={clsx(
                "flex w-full items-center gap-2 rounded px-2 py-1.5 text-sm",
                route === r ? "bg-bg text-text" : "text-muted hover:bg-bg hover:text-text",
              )}
            >
              <Icon size={14} />
              {label}
            </button>
          ))}
        </div>

        {teams.data && teams.data.length > 0 ? (
          <Section title="Teams">
            {teams.data.map((t) => (
              <SidebarRow key={t.id} label={`${t.identifier} · ${t.name}`} />
            ))}
          </Section>
        ) : null}

        {projects.data && projects.data.length > 0 ? (
          <Section title="Projects" action={<Plus size={12} />}>
            {projects.data.map((p) => (
              <SidebarRow key={p.id} label={p.name} />
            ))}
          </Section>
        ) : null}
      </nav>

      <div className="flex items-center gap-2 border-t border-border p-3">
        <Avatar name="You" />
        <span className="truncate text-xs text-muted">{workspaceId || "—"}</span>
      </div>
    </aside>
  );
}

function Section({
  title,
  action,
  children,
}: {
  title: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="mb-4">
      <div className="mb-1 flex items-center justify-between px-2 text-[10px] font-semibold uppercase tracking-wider text-muted">
        <span>{title}</span>
        {action ? <button className="text-muted hover:text-text">{action}</button> : null}
      </div>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

function SidebarRow({ label }: { label: string }) {
  return (
    <button className="flex w-full items-center gap-2 rounded px-2 py-1 text-sm text-muted hover:bg-bg hover:text-text">
      <span className="truncate">{label}</span>
    </button>
  );
}
