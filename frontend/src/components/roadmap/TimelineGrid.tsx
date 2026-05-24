import {
  columnTicks,
  HEADER_HEIGHT,
  LEFT_PANEL_WIDTH,
  xForDate,
  type TimelineConfig,
} from "./timeline";

interface TimelineGridProps {
  cfg: TimelineConfig;
  // Total content height so the today line + grid lines span every
  // row beneath the header.
  height: number;
}

// TimelineGrid renders the date axis (top) + the vertical column
// dividers + the "today" red line. Rendered once per page; ProjectRow
// instances are siblings, not children, so the grid stays inside its
// own SVG layer.
export function TimelineGrid({ cfg, height }: TimelineGridProps) {
  const ticks = columnTicks(cfg);
  const today = xForDate(new Date(), cfg);
  const showToday = today >= 0 && today <= cfg.width;

  return (
    <div className="flex">
      <div
        className="shrink-0 border-b border-r border-border bg-surface"
        style={{ width: LEFT_PANEL_WIDTH, height: HEADER_HEIGHT }}
      />
      <svg
        width={cfg.width}
        height={HEADER_HEIGHT}
        className="block border-b border-border bg-surface"
      >
        {ticks.map((t, i) => (
          <g key={i}>
            <line x1={t.x} x2={t.x} y1={0} y2={HEADER_HEIGHT} stroke="#1d2230" />
            <text
              x={t.x + 6}
              y={HEADER_HEIGHT - 10}
              fontSize={10}
              className="fill-muted"
            >
              {t.label}
            </text>
          </g>
        ))}
      </svg>

      {/* Today line sits on top of everything as a fixed-position
          overlay. width is 1px; the height matches the rows beneath. */}
      {showToday ? (
        <div
          aria-hidden
          className="pointer-events-none absolute"
          style={{
            left: LEFT_PANEL_WIDTH + today,
            top: 0,
            width: 1,
            height,
            background: "#ef4444",
            zIndex: 5,
          }}
        />
      ) : null}
    </div>
  );
}
