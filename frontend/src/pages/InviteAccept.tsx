import { useEffect, useState } from "react";
import { useQuery, useMutation } from "@tanstack/react-query";
import { guestsApi } from "~/api/guests";
import { Button } from "~/components/ui/Button";
import { Input } from "~/components/ui/Input";

interface InviteAcceptProps {
  token: string;
  // onAccepted is fired with the workspace + project IDs from the
  // server response. The caller swaps the page over to GuestView.
  onAccepted: (vars: {
    workspaceID: string;
    projectID?: string;
  }) => void;
}

export function InviteAcceptPage({ token, onAccepted }: InviteAcceptProps) {
  const [name, setName] = useState("");
  const invite = useQuery({
    queryKey: ["invite", token],
    queryFn: () => guestsApi.getInvite(token),
  });
  const accept = useMutation({
    mutationFn: () => guestsApi.accept(token, name),
    onSuccess: (res) => {
      // Persist the signed token in localStorage so subsequent API
      // calls pick it up via the apiRequest Authorization header.
      localStorage.setItem("track_api_key", res.access_token);
      localStorage.setItem("track_workspace_id", res.workspace_id);
      if (res.project_id) {
        localStorage.setItem("track_guest_project_id", res.project_id);
      } else {
        localStorage.removeItem("track_guest_project_id");
      }
      onAccepted({ workspaceID: res.workspace_id, projectID: res.project_id });
    },
  });

  return (
    <div className="flex min-h-screen items-center justify-center bg-bg text-text">
      <div className="w-full max-w-sm space-y-4 rounded-lg border border-border bg-surface p-6 shadow-xl">
        <div className="flex h-8 items-center gap-2">
          <div className="flex h-6 w-6 items-center justify-center rounded bg-accent text-bg">
            <span className="font-mono text-xs font-bold">T</span>
          </div>
          <span className="text-sm font-semibold">Talyvor Track</span>
        </div>

        {invite.isLoading ? (
          <p className="text-sm text-muted">Loading invite…</p>
        ) : invite.error ? (
          <p className="text-sm text-priority-urgent">
            This invitation is no longer valid: {(invite.error as Error).message}
          </p>
        ) : invite.data ? (
          <>
            <div>
              <h1 className="text-lg font-semibold">You've been invited</h1>
              <p className="mt-1 text-xs text-muted">
                Role: <span className="text-text">{invite.data.role}</span>
                {" · "}
                expires {new Date(invite.data.expires_at).toLocaleDateString()}
              </p>
            </div>
            <Input
              placeholder="Your name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <Button
              onClick={() => accept.mutate()}
              disabled={accept.isPending || !name.trim()}
              className="w-full"
            >
              {accept.isPending ? "Accepting…" : "Accept invitation"}
            </Button>
            {accept.error ? (
              <p className="text-xs text-priority-urgent">
                {(accept.error as Error).message}
              </p>
            ) : null}
          </>
        ) : null}

        <FooterBranding />
      </div>
    </div>
  );
}

// FooterBranding is the "Powered by" surface shown on every public
// guest page. Spec explicitly calls it out as a free-marketing
// surface — keep it tasteful, keep it visible.
export function FooterBranding() {
  return (
    <div className="border-t border-border pt-3 text-center text-[10px] text-muted">
      Powered by{" "}
      <a
        href="https://talyvor.com"
        className="font-medium hover:text-text"
        target="_blank"
        rel="noreferrer noopener"
      >
        Talyvor Track
      </a>
    </div>
  );
}

// useInviteToken: simple URL parser. We don't pull in TanStack Router
// just for this — Phase 9's router migration will own routing for the
// whole app at once.
export function useInviteToken(): string | null {
  const [token, setToken] = useState<string | null>(() => parseToken());
  useEffect(() => {
    const onPop = () => setToken(parseToken());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);
  return token;
}

function parseToken(): string | null {
  if (typeof window === "undefined") return null;
  const m = window.location.pathname.match(/^\/invite\/([^/]+)/);
  return m ? m[1] : null;
}
