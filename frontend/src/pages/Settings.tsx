import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, X } from "lucide-react";
import { useWorkspaceStore } from "~/stores/workspace";
import { teamsApi } from "~/api/teams";
import { guestsApi } from "~/api/guests";
import { Input } from "~/components/ui/Input";
import { Button } from "~/components/ui/Button";
import { useUIStore } from "~/stores/ui";
import type { GuestRecord, GuestRole, InviteCreateResponse } from "~/api/types";

export function SettingsPage() {
  const { workspaceId, memberId, teamId, setWorkspaceId, setMemberId, setTeamId } =
    useWorkspaceStore();
  const teams = useQuery({
    queryKey: ["teams", workspaceId],
    queryFn: () => teamsApi.list(workspaceId),
    enabled: !!workspaceId,
  });
  return (
    <div className="max-w-2xl space-y-6 p-6">
      <section>
        <h2 className="mb-2 text-sm font-semibold">Workspace</h2>
        <p className="mb-2 text-xs text-muted">
          Pre-auth-shim. Phase 9 replaces this with a real session.
        </p>
        <Field label="Workspace ID">
          <Input
            value={workspaceId}
            onChange={(e) => setWorkspaceId(e.target.value)}
            placeholder="default"
          />
        </Field>
        <Field label="Your Member ID">
          <Input
            value={memberId}
            onChange={(e) => setMemberId(e.target.value)}
            placeholder="optional"
          />
        </Field>
      </section>
      <section>
        <h2 className="mb-2 text-sm font-semibold">Active team</h2>
        {teams.isLoading ? (
          <p className="text-xs text-muted">Loading teams…</p>
        ) : (teams.data ?? []).length === 0 ? (
          <p className="text-xs text-muted">No teams yet — create one via the API.</p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {teams.data!.map((t) => (
              <Button
                key={t.id}
                variant={t.id === teamId ? "primary" : "secondary"}
                size="sm"
                onClick={() => setTeamId(t.id)}
              >
                {t.identifier} · {t.name}
              </Button>
            ))}
          </div>
        )}
      </section>
      <section>
        <h2 className="mb-2 text-sm font-semibold">API key</h2>
        <p className="mb-2 text-xs text-muted">
          Stored in <code>localStorage.track_api_key</code>. We never log or transmit it elsewhere.
        </p>
        <Input
          defaultValue={localStorage.getItem("track_api_key") ?? ""}
          placeholder="track_…"
          onBlur={(e) => localStorage.setItem("track_api_key", e.target.value)}
        />
      </section>
      <GuestsSection />
    </div>
  );
}

// GuestsSection embeds the guest-management surface in Settings. We
// keep it inline (not a separate page) because it's an admin-style
// list with one expanded "Invite" form — splitting would add nav
// friction without adding value.
function GuestsSection() {
  const { workspaceId } = useWorkspaceStore();
  const qc = useQueryClient();
  const toast = useUIStore((s) => s.toast);

  const guests = useQuery({
    queryKey: ["guests", workspaceId],
    queryFn: () => guestsApi.list(workspaceId),
    enabled: !!workspaceId,
  });
  const revoke = useMutation({
    mutationFn: (id: string) => guestsApi.revoke(workspaceId, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["guests"] }),
    onError: (err: Error) => toast(err.message, "error"),
  });
  const invite = useMutation({
    mutationFn: (vars: { email: string; role: GuestRole }) =>
      guestsApi.invite(workspaceId, vars),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["guests"] }),
    onError: (err: Error) => toast(err.message, "error"),
  });

  const [email, setEmail] = useState("");
  const [role, setRole] = useState<GuestRole>("viewer");
  const [lastInvite, setLastInvite] = useState<InviteCreateResponse | null>(null);

  const submit = () => {
    if (!email.trim()) {
      toast("Email required", "warn");
      return;
    }
    invite.mutate(
      { email: email.trim(), role },
      {
        onSuccess: (res) => {
          setLastInvite(res);
          setEmail("");
        },
      },
    );
  };

  const copy = (text: string) => {
    void navigator.clipboard.writeText(text);
    toast("Copied to clipboard", "success");
  };

  return (
    <section>
      <h2 className="mb-2 text-sm font-semibold">Guests</h2>
      <p className="mb-2 text-xs text-muted">
        Invite clients, contractors, or stakeholders. Free on every plan — no per-seat charges.
      </p>

      <div className="mb-3 flex items-center gap-2 rounded-md border border-border bg-bg/40 p-2">
        <Input
          placeholder="guest@example.com"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <select
          value={role}
          onChange={(e) => setRole(e.target.value as GuestRole)}
          className="h-9 rounded border border-border bg-bg px-2 text-xs"
        >
          <option value="viewer">Viewer</option>
          <option value="commenter">Commenter</option>
          <option value="editor">Editor</option>
        </select>
        <Button size="sm" onClick={submit} disabled={invite.isPending}>
          {invite.isPending ? "Inviting…" : "Invite"}
        </Button>
      </div>

      {lastInvite ? (
        <div className="mb-3 rounded-md border border-accent/40 bg-accent/5 p-3 text-xs">
          <div className="mb-1 font-medium">Invite created</div>
          <div className="mb-2 text-muted">
            Send this link — it expires{" "}
            {new Date(lastInvite.expires_at).toLocaleDateString()}.
          </div>
          <div className="flex items-center gap-2">
            <code className="flex-1 truncate rounded bg-bg px-2 py-1 font-mono text-[10px]">
              {lastInvite.invite_url}
            </code>
            <button
              onClick={() => copy(lastInvite.invite_url)}
              className="text-muted hover:text-text"
              title="Copy"
            >
              <Copy size={12} />
            </button>
          </div>
        </div>
      ) : null}

      <div className="space-y-1">
        {guests.isLoading ? (
          <div className="text-xs text-muted">Loading…</div>
        ) : (guests.data ?? []).length === 0 ? (
          <div className="text-xs text-muted">No guests yet.</div>
        ) : (
          (guests.data ?? []).map((g: GuestRecord) => (
            <div
              key={g.id}
              className="flex items-center gap-2 rounded-md border border-border bg-bg px-2 py-1.5 text-xs"
            >
              <div className="flex-1 truncate">
                <span className="font-medium">{g.name || g.email}</span>
                <span className="ml-1 text-muted">{g.email}</span>
              </div>
              <span className="text-[10px] uppercase tracking-wider text-muted">{g.role}</span>
              {!g.active ? (
                <span className="text-[10px] text-priority-urgent">revoked</span>
              ) : null}
              {g.active ? (
                <button
                  onClick={() => revoke.mutate(g.id)}
                  className="text-muted hover:text-priority-urgent"
                  title="Revoke access"
                >
                  <X size={12} />
                </button>
              ) : null}
            </div>
          ))
        )}
      </div>
    </section>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="mb-3 block">
      <div className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted">
        {label}
      </div>
      {children}
    </label>
  );
}
