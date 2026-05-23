import { useQuery } from "@tanstack/react-query";
import { useWorkspaceStore } from "~/stores/workspace";
import { teamsApi } from "~/api/teams";
import { Input } from "~/components/ui/Input";
import { Button } from "~/components/ui/Button";

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
    </div>
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
