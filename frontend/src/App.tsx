import { useEffect, useState } from "react";
import { Sidebar } from "./components/layout/Sidebar";
import { Header } from "./components/layout/Header";
import { CommandPalette } from "./components/layout/CommandPalette";
import { Toaster } from "./components/ui/Toaster";
import { IssuesPage } from "./pages/Issues";
import { CyclesPage } from "./pages/Cycles";
import { RoadmapPage } from "./pages/Roadmap";
import { TimeReportPage } from "./pages/TimeReport";
import { TemplatesPage } from "./pages/Templates";
import { AnalyticsPage } from "./pages/Analytics";
import { SettingsPage } from "./pages/Settings";
import { useUIStore } from "./stores/ui";
import { useWorkspaceStore } from "./stores/workspace";
import { useWebSocket } from "./hooks/useWebSocket";

// Top-level pages enumerated explicitly. The Issues page hosts both
// the list and the kanban board behind an in-page view toggle, so
// "board" no longer needs its own top-level route.
export type Route =
  | "issues"
  | "cycles"
  | "roadmap"
  | "time"
  | "analytics"
  | "templates"
  | "settings";

const titleByRoute: Record<Route, string> = {
  issues: "Issues",
  cycles: "Cycles",
  roadmap: "Roadmap",
  time: "Time",
  analytics: "Analytics",
  templates: "Templates",
  settings: "Settings",
};

export function App() {
  const [route, setRoute] = useState<Route>("issues");
  const [createOpen, setCreateOpen] = useState(false);
  const commandOpen = useUIStore((s) => s.commandPaletteOpen);
  const setCommandOpen = useUIStore((s) => s.setCommandPaletteOpen);
  const workspaceId = useWorkspaceStore((s) => s.workspaceId);
  const memberId = useWorkspaceStore((s) => s.memberId);

  // Cmd+K / Ctrl+K toggles the command palette. Bound at the App
  // level so it fires regardless of which child is focused.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setCommandOpen(!commandOpen);
      }
      if (e.key === "Escape") {
        setCommandOpen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [commandOpen, setCommandOpen]);

  // Live updates: subscribe to the workspace room. Other components
  // can join issue:/team: rooms as the user navigates into them.
  useWebSocket(workspaceId, memberId);

  return (
    <div className="flex h-screen w-full bg-bg text-text">
      <Sidebar route={route} onNavigate={setRoute} />
      <div className="flex flex-1 flex-col overflow-hidden">
        <Header
          title={titleByRoute[route]}
          onCreate={route === "issues" ? () => setCreateOpen(true) : undefined}
        />
        <main className="flex-1 overflow-auto">
          {route === "issues" && (
            <IssuesPage createOpen={createOpen} setCreateOpen={setCreateOpen} />
          )}
          {route === "cycles" && <CyclesPage />}
          {route === "roadmap" && <RoadmapPage />}
          {route === "time" && <TimeReportPage />}
          {route === "templates" && <TemplatesPage />}
          {route === "analytics" && <AnalyticsPage />}
          {route === "settings" && <SettingsPage />}
        </main>
      </div>
      <CommandPalette onNavigate={(r) => setRoute(r as Route)} />
      <Toaster />
    </div>
  );
}
