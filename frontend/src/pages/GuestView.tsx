import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { guestsApi } from "~/api/guests";
import { StatusBadge } from "~/components/issue/StatusBadge";
import { PriorityIcon } from "~/components/issue/PriorityIcon";
import { AICostBadge } from "~/components/issue/AICostBadge";
import { FooterBranding } from "./InviteAccept";
import type { Issue } from "~/api/types";

interface GuestViewProps {
  workspaceID: string;
  projectID?: string;
}

// Minimal read-only view shown to accepted guests. No sidebar, no
// keyboard shortcuts, no Talyvor admin chrome — just the issues
// list and a detail pane. The visible "Powered by Talyvor Track"
// footer is free marketing surface (per the spec).
export function GuestViewPage({ workspaceID, projectID }: GuestViewProps) {
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const issues = useQuery({
    queryKey: ["guest-issues", workspaceID, projectID],
    queryFn: () => {
      if (projectID) {
        return guestsApi.listIssues(workspaceID, projectID);
      }
      // Workspace-wide guest: no project filter available via the
      // guest endpoints in Phase 7 — fall back to "no issues" until
      // the admin picks a project to share. (Workspace-wide guests
      // are uncommon for this exact reason.)
      return Promise.resolve<Issue[]>([]);
    },
    enabled: !!workspaceID,
  });

  return (
    <div className="flex min-h-screen flex-col bg-bg text-text">
      <header className="flex items-center justify-between border-b border-border bg-surface px-4 py-3">
        <div className="flex items-center gap-2">
          <div className="flex h-6 w-6 items-center justify-center rounded bg-accent text-bg">
            <span className="font-mono text-xs font-bold">T</span>
          </div>
          <span className="text-sm font-semibold">Talyvor Track</span>
        </div>
        <span className="text-[10px] uppercase tracking-wider text-muted">Guest access</span>
      </header>

      <main className="flex flex-1 overflow-hidden">
        <section className="flex-1 overflow-y-auto">
          {issues.isLoading ? (
            <div className="p-8 text-center text-sm text-muted">Loading issues…</div>
          ) : issues.error ? (
            <div className="p-8 text-center text-sm text-priority-urgent">
              {(issues.error as Error).message}
            </div>
          ) : (issues.data ?? []).length === 0 ? (
            <div className="p-8 text-center text-sm text-muted">
              {projectID
                ? "No issues to show yet."
                : "Your guest access doesn't include a specific project. Ask your inviter for a project-scoped link."}
            </div>
          ) : (
            <div>
              {(issues.data ?? []).map((iss) => (
                <button
                  key={iss.id}
                  onClick={() => setSelectedID(iss.id)}
                  className={
                    "flex w-full items-center gap-3 border-b border-border px-4 py-2 text-left hover:bg-surface " +
                    (selectedID === iss.id ? "bg-surface" : "")
                  }
                >
                  <PriorityIcon priority={iss.priority} />
                  <span className="w-20 shrink-0 font-mono text-xs text-muted">
                    {iss.identifier}
                  </span>
                  <StatusBadge status={iss.status} />
                  <span className="flex-1 truncate text-sm">{iss.title}</span>
                  <AICostBadge costUSD={iss.ai_cost_usd ?? 0} tokens={iss.ai_tokens ?? 0} />
                </button>
              ))}
            </div>
          )}
        </section>

        {selectedID ? (
          <aside className="w-1/2 min-w-[360px] max-w-xl overflow-y-auto border-l border-border bg-surface">
            <GuestIssueDetail
              workspaceID={workspaceID}
              issueID={selectedID}
              onClose={() => setSelectedID(null)}
            />
          </aside>
        ) : null}
      </main>

      <footer className="border-t border-border bg-surface px-4 py-3">
        <FooterBranding />
      </footer>
    </div>
  );
}

function GuestIssueDetail({
  workspaceID,
  issueID,
  onClose,
}: {
  workspaceID: string;
  issueID: string;
  onClose: () => void;
}) {
  const detail = useQuery({
    queryKey: ["guest-issue", workspaceID, issueID],
    queryFn: () => guestsApi.getIssue(workspaceID, issueID),
  });

  // Re-fetch whenever issueID changes — query key already covers it,
  // but the useEffect makes the cleanup intent explicit when the
  // pane closes.
  useEffect(() => () => undefined, [issueID]);

  if (detail.isLoading) {
    return <div className="p-6 text-sm text-muted">Loading…</div>;
  }
  if (detail.error || !detail.data) {
    return (
      <div className="p-6 text-sm text-priority-urgent">
        Couldn't load issue.
        <button onClick={onClose} className="ml-2 text-muted underline">
          Close
        </button>
      </div>
    );
  }
  const i = detail.data;
  return (
    <div className="space-y-3 p-6">
      <div className="flex items-center gap-2 text-xs text-muted">
        <span className="font-mono">{i.identifier}</span>
        <AICostBadge costUSD={i.ai_cost_usd ?? 0} tokens={i.ai_tokens ?? 0} />
        <button onClick={onClose} className="ml-auto text-muted underline">
          Close
        </button>
      </div>
      <h2 className="text-lg font-semibold">{i.title}</h2>
      <div className="flex items-center gap-2 text-xs">
        <StatusBadge status={i.status} withLabel />
        <PriorityIcon priority={i.priority} withLabel />
      </div>
      {i.description ? (
        <p className="whitespace-pre-wrap text-sm text-muted">{i.description}</p>
      ) : (
        <p className="text-sm text-muted">No description.</p>
      )}
    </div>
  );
}
