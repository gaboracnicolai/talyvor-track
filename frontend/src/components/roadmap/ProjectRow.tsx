import { Sparkles } from "lucide-react";
import type { RoadmapProject } from "~/api/types";
import { MilestoneMarker } from "./MilestoneMarker";
import {
  LEFT_PANEL_WIDTH,
  projectColor,
  ROW_HEIGHT,
  xForDate,
  type TimelineConfig,
} from "./timeline";

interface ProjectRowProps {
  project: RoadmapProject;
  cfg: TimelineConfig;
  onClick?: () => void;
}

// One full row: left panel (name + team + completion) plus the
// timeline portion (bar + milestone diamonds + AI cost badge). The
// bar is a plain rounded <rect>; the completion fill is a second
// <rect> with a clip path to the same shape.
export function ProjectRow({ project, cfg, onClick }: ProjectRowProps) {
  const startDate = project.start_date ? new Date(project.start_date) : null;
  const endDate = project.target_date ? new Date(project.target_date) : null;
  const hasDates = !!(startDate && endDate);
  const barX = startDate ? xForDate(startDate, cfg) : 0;
  const barEnd = endDate ? xForDate(endDate, cfg) : 0;
  const barWidth = Math.max(8, barEnd - barX);
  const completion = Math.max(0, Math.min(100, project.completion_pct));
  const fill = projectColor(project.status);

  return (
    <div
      className="flex border-b border-border"
      style={{ height: ROW_HEIGHT }}
      onClick={onClick}
    >
      <div
        className="flex shrink-0 cursor-pointer flex-col justify-center border-r border-border bg-surface px-3 hover:bg-bg/60"
        style={{ width: LEFT_PANEL_WIDTH }}
      >
        <div className="truncate text-sm font-medium">{project.name}</div>
        <div className="flex items-center justify-between text-[10px] text-muted">
          <span>{project.team_name}</span>
          <span>{Math.round(completion)}%</span>
        </div>
      </div>

      <div className="relative flex-1">
        <svg
          width={cfg.width}
          height={ROW_HEIGHT}
          className="block"
          role="img"
          aria-label={`Timeline for ${project.name}`}
        >
          {hasDates ? (
            <g>
              <rect
                x={barX}
                y={ROW_HEIGHT / 2 - 10}
                width={barWidth}
                height={20}
                rx={4}
                fill={fill}
                opacity={0.25}
              />
              <rect
                x={barX}
                y={ROW_HEIGHT / 2 - 10}
                width={(barWidth * completion) / 100}
                height={20}
                rx={4}
                fill={fill}
              />
              <text
                x={barX + 8}
                y={ROW_HEIGHT / 2 + 4}
                fontSize={10}
                className="fill-text font-medium"
              >
                {project.name}
              </text>
            </g>
          ) : null}

          {project.milestones.map((m) =>
            m.target_date ? (
              <MilestoneMarker
                key={m.id}
                milestone={m}
                x={xForDate(new Date(m.target_date), cfg)}
              />
            ) : null,
          )}
        </svg>

        {project.ai_cost_usd > 0 && hasDates ? (
          <div
            className="pointer-events-none absolute flex items-center gap-1 rounded-full border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-[10px] font-medium text-accent"
            style={{
              left: Math.min(barX + barWidth + 6, cfg.width - 80),
              top: ROW_HEIGHT / 2 - 9,
            }}
            title={`${project.ai_cost_usd.toFixed(2)} USD via Talyvor Lens`}
          >
            <Sparkles size={10} />${project.ai_cost_usd.toFixed(2)}
          </div>
        ) : null}
      </div>
    </div>
  );
}
