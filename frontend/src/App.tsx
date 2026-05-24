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
import { PrioritizedBacklogPage } from "./pages/PrioritizedBacklog";
import { AnalyticsPage } from "./pages/Analytics";
import { SettingsPage } from "./pages/Settings";
import { InviteAcceptPage, useInviteToken } from "./pages/InviteAccept";
import { GuestViewPage } from "./pages/GuestView";
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
  | "prioritize"
  | "time"
  | "analytics"
  | "templates"
  | "settings";

const titleByRoute: Record<Route, string> = {
  issues: "Issues",
  cycles: "Cycles",
  roadmap: "Roadmap",
  prioritize: "Prioritize",
  time: "Time",
  analytics: "Analytics",
  templates: "Templates",
  settings: "Settings",
};

// guestSessionKey stores whether the current browser is a guest
// session. We persist it so the user doesn't get bounced back to
// the admin shell every reload.
const guestSessionKey = "track_guest_session";

function readGuestSession(): { workspaceID: string; projectID?: string } | null {
  try {
    const raw = localStorage.getItem(guestSessionKey);
    if (!raw) return null;
    return JSON.parse(raw);
  } catch {
    return null;
  }
}
function writeGuestSession(s: { workspaceID: string; projectID?: string }) {
  localStorage.setItem(guestSessionKey, JSON.stringify(s));
}

export function App() {
  const inviteToken = useInviteToken();
  const [guestSession, setGuestSession] = useState<{
    workspaceID: string;
    projectID?: string;
  } | null>(() => readGuestSession());

  if (inviteToken) {
    return (
      <InviteAcceptPage
        token={inviteToken}
        onAccepted={(vars) => {
          // Strip /invite/<token> from the URL so a refresh doesn't
          // re-trigger the accept flow.
          window.history.replaceState({}, "", "/");
          writeGuestSession(vars);
          setGuestSession(vars);
        }}
      />
    );
  }
  if (guestSession) {
    return <GuestViewPage {...guestSession} />;
  }
  return <AdminApp />;
}

// AdminApp is the regular member-facing shell. Split out of `App` so
// the hooks here aren't conditionally evaluated alongside the
// InviteAcceptPage/GuestViewPage branches above — keeps React's
// "same hooks every render" invariant honest.
function AdminApp() {
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
          {route === "prioritize" && <PrioritizedBacklogPage />}
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
