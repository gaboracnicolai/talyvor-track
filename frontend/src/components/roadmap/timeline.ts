// Shared layout helpers for the roadmap.
//
// The roadmap is built as plain SVG inside flex containers — pixel
// positions are computed from JS so we don't depend on any layout
// library. Keeping these helpers in one file means the math is
// testable and consistent across TimelineGrid / ProjectRow /
// MilestoneMarker.

export type ZoomLevel = "week" | "month" | "quarter";

export interface TimelineConfig {
  zoom: ZoomLevel;
  // Inclusive [start, end) span the timeline renders.
  start: Date;
  end: Date;
  // Pixel width of the full timeline area. Computed once per render
  // from the container width so resize behaves.
  width: number;
}

// columnCount: how many ticks (week / month / quarter) the timeline
// shows at the chosen zoom. Matches the default ranges in the spec:
// week → 12 weeks, month → 6 months, quarter → 4 quarters.
export function columnCount(zoom: ZoomLevel): number {
  switch (zoom) {
    case "week":
      return 12;
    case "quarter":
      return 4;
    default:
      return 6; // month
  }
}

// endFromStart builds the inclusive end Date for a fresh timeline,
// given a `start` and a zoom. Used when the page first mounts and
// when the user flips zoom levels (the start date sticks).
export function endFromStart(start: Date, zoom: ZoomLevel): Date {
  const d = new Date(start);
  switch (zoom) {
    case "week":
      d.setDate(d.getDate() + columnCount(zoom) * 7);
      return d;
    case "quarter":
      d.setMonth(d.getMonth() + columnCount(zoom) * 3);
      return d;
    default:
      d.setMonth(d.getMonth() + columnCount(zoom));
      return d;
  }
}

// xForDate maps a calendar date to an absolute pixel coordinate
// inside the timeline. Linear interpolation between [start, end) —
// dates outside the range are clamped at the boundary so a project
// that started before the visible window still shows up at x = 0.
export function xForDate(date: Date, cfg: TimelineConfig): number {
  const total = cfg.end.getTime() - cfg.start.getTime();
  if (total <= 0) return 0;
  const offset = date.getTime() - cfg.start.getTime();
  const ratio = Math.max(0, Math.min(1, offset / total));
  return ratio * cfg.width;
}

// columnTicks returns the label + x position for each grid column.
// Labels are short — "Week of …", "Jan", "Q1 26" — so we don't need
// a tooltip layer over the axis.
export interface Tick {
  x: number;
  label: string;
}

export function columnTicks(cfg: TimelineConfig): Tick[] {
  const out: Tick[] = [];
  const total = columnCount(cfg.zoom);
  for (let i = 0; i < total; i++) {
    const tickDate = new Date(cfg.start);
    if (cfg.zoom === "week") {
      tickDate.setDate(tickDate.getDate() + i * 7);
    } else if (cfg.zoom === "quarter") {
      tickDate.setMonth(tickDate.getMonth() + i * 3);
    } else {
      tickDate.setMonth(tickDate.getMonth() + i);
    }
    out.push({ x: xForDate(tickDate, cfg), label: tickLabel(tickDate, cfg.zoom) });
  }
  return out;
}

function tickLabel(d: Date, zoom: ZoomLevel): string {
  if (zoom === "week") {
    return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  }
  if (zoom === "quarter") {
    const q = Math.floor(d.getMonth() / 3) + 1;
    const yy = String(d.getFullYear()).slice(2);
    return `Q${q} ${yy}`;
  }
  return d.toLocaleDateString(undefined, { month: "short" });
}

// projectColor picks a bar fill based on the project's lifecycle
// status. Matches the colour tokens used elsewhere in the app.
export function projectColor(status: string): string {
  switch (status) {
    case "completed":
      return "#22c55e";
    case "planned":
    case "upcoming":
      return "#5b6471";
    case "cancelled":
      return "#9ca3af";
    default:
      return "#3b82f6"; // active / in-progress
  }
}

// milestoneColor picks a fill for the diamond marker based on the
// milestone status. Overdue (target_date < today AND not completed)
// is computed by the caller because it needs both fields.
export function milestoneColor(status: string, overdue: boolean): string {
  if (status === "completed") return "#22c55e";
  if (overdue) return "#ef4444";
  return "#7a8294";
}

// Row layout constants. Shared so column headers and rows align.
export const ROW_HEIGHT = 56;
export const HEADER_HEIGHT = 32;
export const LEFT_PANEL_WIDTH = 240;
