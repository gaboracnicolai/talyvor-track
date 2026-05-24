import { useMemo, useState } from "react";
import { useRoadmap } from "~/hooks/useRoadmap";
import { TimelineGrid } from "~/components/roadmap/TimelineGrid";
import { ProjectRow } from "~/components/roadmap/ProjectRow";
import {
  columnCount,
  endFromStart,
  HEADER_HEIGHT,
  LEFT_PANEL_WIDTH,
  ROW_HEIGHT,
  type TimelineConfig,
  type ZoomLevel,
} from "~/components/roadmap/timeline";
import clsx from "clsx";
import type { RoadmapProject } from "~/api/types";

const zoomOptions: { value: ZoomLevel; label: string }[] = [
  { value: "week", label: "Week" },
  { value: "month", label: "Month" },
  { value: "quarter", label: "Quarter" },
];

// Width budget per timeline column. With 6 months at 140 px each the
// timeline naturally wants ~840 px — narrower viewports get a
// horizontal scrollbar, which matches the spec's "Works on mobile
// (horizontal scroll)" expectation.
const columnPixelWidth: Record<ZoomLevel, number> = {
  week: 110,
  month: 140,
  quarter: 200,
};

export function RoadmapPage() {
  const [zoom, setZoom] = useState<ZoomLevel>("month");
  const [start] = useState(() => startOfMonth(new Date()));
  const end = useMemo(() => endFromStart(start, zoom), [start, zoom]);

  // The timeline pixel width is computed from the zoom level so the
  // bars line up cleanly with the column ticks at every zoom.
  const width = columnPixelWidth[zoom] * columnCount(zoom);

  const { data, isLoading, error } = useRoadmap({
    start_date: start.toISOString(),
    end_date: end.toISOString(),
  });

  const cfg: TimelineConfig = { zoom, start, end, width };

  // Split scheduled vs unscheduled. Unscheduled rows live in their
  // own section below the timeline since they have nothing useful to
  // place against the date axis.
  const { scheduled, unscheduled } = useMemo(() => {
    const projects = data?.projects ?? [];
    const sch: RoadmapProject[] = [];
    const un: RoadmapProject[] = [];
    for (const p of projects) {
      if (p.start_date || p.target_date) sch.push(p);
      else un.push(p);
    }
    return { scheduled: sch, unscheduled: un };
  }, [data?.projects]);

  const totalRows = scheduled.length;
  const contentHeight = HEADER_HEIGHT + totalRows * ROW_HEIGHT;

  return (
    <div className="flex h-full flex-col">
      <Header zoom={zoom} setZoom={setZoom} />
      <div className="flex-1 overflow-auto">
        <div
          className="relative"
          style={{
            minWidth: LEFT_PANEL_WIDTH + width,
            minHeight: contentHeight,
          }}
        >
          <TimelineGrid cfg={cfg} height={contentHeight} />
          {isLoading ? (
            <div className="p-8 text-center text-sm text-muted">Loading roadmap…</div>
          ) : error ? (
            <div className="p-8 text-center text-sm text-priority-urgent">
              Failed to load: {(error as Error).message}
            </div>
          ) : scheduled.length === 0 ? (
            <EmptyState message="No scheduled projects in this window." />
          ) : (
            scheduled.map((p) => <ProjectRow key={p.id} project={p} cfg={cfg} />)
          )}
        </div>

        {unscheduled.length > 0 ? (
          <UnscheduledSection projects={unscheduled} />
        ) : null}
      </div>
    </div>
  );
}

function Header({ zoom, setZoom }: { zoom: ZoomLevel; setZoom: (z: ZoomLevel) => void }) {
  return (
    <div className="flex items-center justify-between border-b border-border bg-surface px-4 py-2">
      <div className="text-sm font-semibold">Roadmap</div>
      <div className="flex items-center gap-1">
        {zoomOptions.map((opt) => (
          <button
            key={opt.value}
            onClick={() => setZoom(opt.value)}
            className={clsx(
              "h-7 rounded px-2 text-xs",
              zoom === opt.value
                ? "bg-bg text-text"
                : "text-muted hover:bg-bg/50 hover:text-text",
            )}
          >
            {opt.label}
          </button>
        ))}
      </div>
    </div>
  );
}

function EmptyState({ message }: { message: string }) {
  return <div className="p-8 text-center text-sm text-muted">{message}</div>;
}

function UnscheduledSection({ projects }: { projects: RoadmapProject[] }) {
  return (
    <div className="border-t border-border">
      <div className="bg-surface px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted">
        Unscheduled
      </div>
      <div className="divide-y divide-border">
        {projects.map((p) => (
          <UnscheduledRow key={p.id} project={p} />
        ))}
      </div>
    </div>
  );
}

function UnscheduledRow({ project }: { project: RoadmapProject }) {
  // Carry the same shape as scheduled rows (left panel + AI cost
  // badge) so the visual is consistent. Just no timeline bar.
  return (
    <div className="flex items-center gap-3 px-4 py-2 text-xs">
      <div className="w-56 truncate font-medium">{project.name}</div>
      <div className="text-muted">{project.team_name}</div>
      <div className="ml-auto flex items-center gap-2">
        <span className="text-muted">
          {project.completed_count}/{project.issue_count}
        </span>
        {project.ai_cost_usd > 0 ? (
          <span
            className="rounded-full border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-[10px] font-medium text-accent"
            title="AI spend attributed to this project"
          >
            ${project.ai_cost_usd.toFixed(2)}
          </span>
        ) : null}
      </div>
    </div>
  );
}

// startOfMonth lets the roadmap start exactly on the first of the
// current month — keeps the visible grid aligned with the natural
// month boundaries.
function startOfMonth(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), 1);
}

