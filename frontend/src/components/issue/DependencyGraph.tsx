import { useMemo, useState } from "react";
import { ZoomIn, ZoomOut, Maximize2 } from "lucide-react";
import { useDependencyGraph } from "~/hooks/useRelations";
import { useUIStore } from "~/stores/ui";
import type { GraphEdge, GraphNode, RelationType } from "~/api/types";

interface DependencyGraphProps {
  issueID: string;
  depth?: number;
}

// Lightweight SVG renderer. Layout is a deterministic concentric-ring
// algorithm: root in the centre, every other node placed on a ring
// at angle 2π × index/N. Zoom is a CSS transform on the inner <g>
// wrapper — no re-layout per zoom step. Selection highlights direct
// connections (the node + every edge touching it); double-click on a
// node navigates to the issue detail pane.
export function DependencyGraph({ issueID, depth = 3 }: DependencyGraphProps) {
  const { data, isLoading } = useDependencyGraph(issueID, depth);
  const focusIssue = useUIStore((s) => s.setSelectedIssueId);
  const [scale, setScale] = useState(1);
  const [highlightedID, setHighlightedID] = useState<string | null>(null);

  const layout = useMemo(() => {
    if (!data) return null;
    return computeLayout(data.nodes, data.edges, issueID);
  }, [data, issueID]);

  if (isLoading) {
    return <div className="p-6 text-center text-xs text-muted">Loading graph…</div>;
  }
  if (!layout || layout.nodes.length === 0) {
    return (
      <div className="p-6 text-center text-xs text-muted">No dependencies yet.</div>
    );
  }

  const width = 600;
  const height = 400;
  const connectedIDs = highlightedID
    ? connectedNodeIDs(highlightedID, layout.edges)
    : null;

  return (
    <div className="relative rounded-md border border-border bg-bg">
      <ZoomControls
        scale={scale}
        onIn={() => setScale((s) => Math.min(2, s + 0.2))}
        onOut={() => setScale((s) => Math.max(0.4, s - 0.2))}
        onFit={() => setScale(1)}
      />
      <svg
        viewBox={`0 0 ${width} ${height}`}
        className="block w-full"
        role="img"
        aria-label="Dependency graph"
      >
        <defs>
          {/* One marker per edge colour so arrowheads match their
              line. Pre-defined here so paint cost is fixed. */}
          <ArrowMarker id="arrow-grey" color="#7a8294" />
          <ArrowMarker id="arrow-red" color="#ef4444" />
          <ArrowMarker id="arrow-purple" color="#a78bfa" />
          <ArrowMarker id="arrow-green" color="#22c55e" />
        </defs>
        <g transform={`translate(${(width * (1 - scale)) / 2}, ${(height * (1 - scale)) / 2}) scale(${scale})`}>
          {layout.edges.map((e, i) => {
            const dim =
              connectedIDs && !connectedIDs.has(e.from.id) && !connectedIDs.has(e.to.id);
            return (
              <line
                key={i}
                x1={e.from.x}
                y1={e.from.y}
                x2={e.to.x}
                y2={e.to.y}
                stroke={edgeStroke(e.type)}
                strokeWidth={1.5}
                strokeOpacity={dim ? 0.2 : 1}
                strokeDasharray={e.type === "relates_to" ? "4 3" : undefined}
                markerEnd={`url(#${markerID(e.type)})`}
              />
            );
          })}
          {layout.nodes.map((n) => {
            const dim = connectedIDs && !connectedIDs.has(n.id);
            const isRoot = n.id === issueID;
            const isHighlighted = highlightedID === n.id;
            return (
              <g
                key={n.id}
                transform={`translate(${n.x}, ${n.y})`}
                onClick={() => setHighlightedID(isHighlighted ? null : n.id)}
                onDoubleClick={() => focusIssue(n.id)}
                style={{ cursor: "pointer", opacity: dim ? 0.3 : 1 }}
              >
                <circle
                  r={isRoot ? 22 : 16}
                  fill={statusColor(n.status)}
                  stroke={isHighlighted ? "#f0a030" : isRoot ? "#f0a030" : "#1d2230"}
                  strokeWidth={isHighlighted || isRoot ? 2 : 1}
                />
                <text
                  y={-2}
                  textAnchor="middle"
                  className="fill-text"
                  fontSize={9}
                  fontFamily="IBM Plex Mono"
                  pointerEvents="none"
                >
                  {n.identifier}
                </text>
                <text
                  y={9}
                  textAnchor="middle"
                  className="fill-muted"
                  fontSize={8}
                  pointerEvents="none"
                >
                  {truncate(n.title, 14)}
                </text>
              </g>
            );
          })}
        </g>
      </svg>
      <div className="border-t border-border bg-surface px-3 py-1.5 text-[10px] text-muted">
        Click a node to highlight its direct connections. Double-click to open the issue.
      </div>
    </div>
  );
}

function ArrowMarker({ id, color }: { id: string; color: string }) {
  return (
    <marker
      id={id}
      viewBox="0 0 10 10"
      refX="10"
      refY="5"
      markerWidth="6"
      markerHeight="6"
      orient="auto-start-reverse"
    >
      <path d="M 0 0 L 10 5 L 0 10 z" fill={color} />
    </marker>
  );
}

function ZoomControls({
  scale,
  onIn,
  onOut,
  onFit,
}: {
  scale: number;
  onIn: () => void;
  onOut: () => void;
  onFit: () => void;
}) {
  return (
    <div className="absolute right-2 top-2 z-10 inline-flex rounded-md border border-border bg-surface shadow">
      <button onClick={onOut} className="h-7 w-7 text-muted hover:text-text" title="Zoom out">
        <ZoomOut size={12} className="mx-auto" />
      </button>
      <button onClick={onFit} className="h-7 px-2 text-[10px] text-muted hover:text-text" title="Fit to screen">
        {Math.round(scale * 100)}%
      </button>
      <button onClick={onIn} className="h-7 w-7 text-muted hover:text-text" title="Zoom in">
        <ZoomIn size={12} className="mx-auto" />
      </button>
      <button onClick={onFit} className="h-7 w-7 text-muted hover:text-text" title="Reset zoom">
        <Maximize2 size={12} className="mx-auto" />
      </button>
    </div>
  );
}

// connectedNodeIDs walks the edges to find every node directly linked
// to the chosen one. Used to dim everything else when a node is
// selected so the user can trace a chain visually.
function connectedNodeIDs(rootID: string, edges: { from: PositionedNode; to: PositionedNode; type: RelationType }[]): Set<string> {
  const out = new Set<string>([rootID]);
  for (const e of edges) {
    if (e.from.id === rootID) out.add(e.to.id);
    if (e.to.id === rootID) out.add(e.from.id);
  }
  return out;
}

interface PositionedNode extends GraphNode {
  x: number;
  y: number;
}

function computeLayout(nodes: GraphNode[], edges: GraphEdge[], rootID: string) {
  const positioned: PositionedNode[] = [];
  const root = nodes.find((n) => n.id === rootID);
  if (!root) {
    return { nodes: [], edges: [] };
  }
  positioned.push({ ...root, x: 300, y: 200 });

  const others = nodes.filter((n) => n.id !== rootID);
  others.forEach((n, i) => {
    const angle = (2 * Math.PI * i) / Math.max(others.length, 1);
    positioned.push({ ...n, x: 300 + 160 * Math.cos(angle), y: 200 + 130 * Math.sin(angle) });
  });

  const byID = new Map(positioned.map((n) => [n.id, n]));
  const layoutEdges = edges
    .map((e) => {
      const from = byID.get(e.source);
      const to = byID.get(e.target);
      if (!from || !to) return null;
      return { from, to, type: e.type };
    })
    .filter((e): e is { from: PositionedNode; to: PositionedNode; type: RelationType } => !!e);

  return { nodes: positioned, edges: layoutEdges };
}

function statusColor(status: string): string {
  switch (status) {
    case "backlog":
      return "#3d4250";
    case "todo":
      return "#5b6471";
    case "in_progress":
    case "in_review":
      return "#3b82f6";
    case "done":
      return "#22c55e";
    case "cancelled":
      return "#9ca3af";
    default:
      return "#3d4250";
  }
}

function edgeStroke(type: RelationType): string {
  switch (type) {
    case "blocks":
    case "blocked_by":
      return "#ef4444";
    case "duplicates":
      return "#a78bfa";
    case "clones":
      return "#22c55e";
    default:
      return "#7a8294";
  }
}

function markerID(type: RelationType): string {
  switch (type) {
    case "blocks":
    case "blocked_by":
      return "arrow-red";
    case "duplicates":
      return "arrow-purple";
    case "clones":
      return "arrow-green";
    default:
      return "arrow-grey";
  }
}

function truncate(s: string, max: number): string {
  return s.length > max ? s.slice(0, max - 1) + "…" : s;
}
