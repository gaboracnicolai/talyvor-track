import { useMemo, useState } from "react";
import { Download, Clock, DollarSign } from "lucide-react";
import { Button } from "~/components/ui/Button";
import {
  formatDuration,
  useWorkspaceTimeSummary,
} from "~/hooks/useTimeTracking";
import type { WorkspaceTimeSummary } from "~/api/types";

const presets: { label: string; days: number }[] = [
  { label: "Last 7 days", days: 7 },
  { label: "Last 30 days", days: 30 },
  { label: "Last 90 days", days: 90 },
];

export function TimeReportPage() {
  const [days, setDays] = useState(30);
  const since = useMemo(() => {
    const d = new Date();
    d.setUTCDate(d.getUTCDate() - days);
    return d.toISOString();
  }, [days]);
  const { data, isLoading, error } = useWorkspaceTimeSummary(since);

  return (
    <div className="space-y-4 p-4">
      <Header
        days={days}
        setDays={setDays}
        disabled={isLoading || !data}
        onExport={() => data && downloadCSV(data, days)}
      />
      {error ? (
        <div className="rounded-md border border-priority-urgent/40 bg-priority-urgent/10 p-4 text-sm text-priority-urgent">
          Failed to load: {(error as Error).message}
        </div>
      ) : !data ? (
        <div className="text-sm text-muted">Loading…</div>
      ) : (
        <>
          <Totals summary={data} />
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <Section title="By member">
              {data.by_member.length === 0 ? (
                <Empty />
              ) : (
                <RowList
                  rows={data.by_member.map((m) => ({
                    label: m.name,
                    total: m.total_sec,
                    billable: m.billable_sec,
                  }))}
                />
              )}
            </Section>
            <Section title="By project">
              {data.by_project.length === 0 ? (
                <Empty />
              ) : (
                <RowList
                  rows={data.by_project.map((p) => ({
                    label: p.name,
                    total: p.total_sec,
                    billable: p.billable_sec,
                  }))}
                />
              )}
            </Section>
          </div>
        </>
      )}
    </div>
  );
}

function Header({
  days,
  setDays,
  disabled,
  onExport,
}: {
  days: number;
  setDays: (d: number) => void;
  disabled: boolean;
  onExport: () => void;
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-surface px-3 py-2">
      <div className="flex items-center gap-1">
        {presets.map((p) => (
          <button
            key={p.days}
            onClick={() => setDays(p.days)}
            className={
              days === p.days
                ? "h-7 rounded bg-bg px-2 text-xs text-text"
                : "h-7 rounded px-2 text-xs text-muted hover:bg-bg/50 hover:text-text"
            }
          >
            {p.label}
          </button>
        ))}
      </div>
      <Button size="sm" variant="secondary" onClick={onExport} disabled={disabled}>
        <Download size={12} /> Export CSV
      </Button>
    </div>
  );
}

function Totals({ summary }: { summary: WorkspaceTimeSummary }) {
  const internal = summary.total_sec - summary.billable_sec;
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
      <Kpi
        icon={<Clock size={14} className="text-muted" />}
        label="Total tracked"
        value={formatDuration(summary.total_sec)}
      />
      <Kpi
        icon={<DollarSign size={14} className="text-status-done" />}
        label="Billable"
        value={formatDuration(summary.billable_sec)}
      />
      <Kpi
        icon={<Clock size={14} className="text-muted" />}
        label="Internal"
        value={formatDuration(Math.max(0, internal))}
      />
    </div>
  );
}

function Kpi({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
}) {
  return (
    <div className="rounded-md border border-border bg-surface p-3">
      <div className="flex items-center gap-1 text-[10px] uppercase tracking-wider text-muted">
        {icon}
        {label}
      </div>
      <div className="mt-1 font-mono text-xl">{value}</div>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-md border border-border bg-surface p-4">
      <h3 className="mb-2 text-sm font-semibold">{title}</h3>
      {children}
    </div>
  );
}

function Empty() {
  return <div className="py-6 text-center text-xs text-muted">No data in this window.</div>;
}

function RowList({
  rows,
}: {
  rows: { label: string; total: number; billable: number }[];
}) {
  const max = Math.max(...rows.map((r) => r.total), 1);
  return (
    <div className="space-y-2">
      {rows.map((r) => (
        <div key={r.label} className="text-xs">
          <div className="flex justify-between">
            <span>{r.label}</span>
            <span className="text-muted">
              {formatDuration(r.total)}
              {r.billable !== r.total ? (
                <span className="ml-1 text-status-done">({formatDuration(r.billable)} bill.)</span>
              ) : null}
            </span>
          </div>
          <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-bg">
            <div
              className="h-full bg-accent"
              style={{ width: `${(r.total / max) * 100}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

// downloadCSV emits a flat sheet with two sections: by-member and
// by-project rollups. RFC 4180 CSV — comma-separated, double-quote
// escaping for any field containing a comma or quote.
function downloadCSV(summary: WorkspaceTimeSummary, days: number) {
  const lines: string[] = [];
  lines.push("Time report");
  lines.push(`Window,Last ${days} days`);
  lines.push(`Total seconds,${summary.total_sec}`);
  lines.push(`Billable seconds,${summary.billable_sec}`);
  lines.push("");
  lines.push("By member");
  lines.push("Member,Total seconds,Billable seconds");
  for (const m of summary.by_member) {
    lines.push([csvField(m.name), m.total_sec, m.billable_sec].join(","));
  }
  lines.push("");
  lines.push("By project");
  lines.push("Project,Total seconds,Billable seconds");
  for (const p of summary.by_project) {
    lines.push([csvField(p.name), p.total_sec, p.billable_sec].join(","));
  }
  const blob = new Blob([lines.join("\n")], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `talyvor-time-report-${new Date().toISOString().slice(0, 10)}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

function csvField(s: string): string {
  if (/[,"\n]/.test(s)) {
    return `"${s.replace(/"/g, '""')}"`;
  }
  return s;
}
