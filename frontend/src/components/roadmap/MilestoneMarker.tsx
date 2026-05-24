import type { RoadmapMilestone } from "~/api/types";
import { milestoneColor, ROW_HEIGHT } from "./timeline";

interface MilestoneMarkerProps {
  milestone: RoadmapMilestone;
  x: number;
}

// Diamond marker rendered as an SVG <polygon>. The hover tooltip is
// a plain HTML <title> so screen readers + native UA tooltips work
// without us building a custom overlay.
export function MilestoneMarker({ milestone, x }: MilestoneMarkerProps) {
  if (!milestone.target_date) return null;
  const overdue =
    milestone.status !== "completed" &&
    new Date(milestone.target_date).getTime() < Date.now();
  const fill = milestoneColor(milestone.status, overdue);
  const y = ROW_HEIGHT / 2;
  const size = 7;
  const tooltip = [
    milestone.name,
    new Date(milestone.target_date).toLocaleDateString(),
    `${milestone.completed_count}/${milestone.issue_count} done`,
    milestone.ai_cost_usd > 0 ? `$${milestone.ai_cost_usd.toFixed(2)} AI` : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <g transform={`translate(${x}, ${y})`} aria-label={tooltip} role="img">
      <polygon
        points={`0,-${size} ${size},0 0,${size} -${size},0`}
        fill={fill}
        stroke="#0c0e12"
        strokeWidth={1}
      />
      <title>{tooltip}</title>
    </g>
  );
}
