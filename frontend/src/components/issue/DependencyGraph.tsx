import { useMemo } from "react";
import { useDependencyGraph } from "~/hooks/useRelations";
import { useUIStore } from "~/stores/ui";
import type { GraphEdge, GraphNode, RelationType } from "~/api/types";

interface DependencyGraphProps {
  issueID: string;
  depth?: number;
}

// Lightweight SVG renderer. Layout is a deterministic concentric-ring
// algorithm: root in the centre, every other node placed on a ring
// at angle 2π × index/N. No physics simulation — at the workspace
// sizes the API caps at (≤ MaxGraphDepth=5 layers), the visual is
// already good enough, and the static positions keep the chart from
// jiggling on every re-render.
export function DependencyGraph({ issueID, depth = 3 }: DependencyGraphProps) {
  const { data, isLoading } = useDependencyGraph(issueID, depth);
  const focusIssue = useUIStore((s) => s.setSelectedIssueId);

  const layout = useMemo(() => {
    if (!data) return null;
    return computeLayout(data.nodes, data.edges, issueID);
  }, [data, issueID]);

  if (isLoading) {
    return <div className="p-6 text-center text-xs text-muted">Loading graph…</div>;
  }
  if (!layout || layout.nodes.length === 0) {
    return (
      <div className="p-6 text-center text-xs text-muted">
        No dependencies yet.
      </div>
    );
  }

  const width = 600;
  const height = 400;

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      className="w-full rounded-md border border-border bg-bg"
      role="img"
      aria-label="Dependency graph"
    >
      {/* Edges first so node circles draw over them. */}
      {layout.edges.map((e, i) => (
        <line
          key={i}
          x1={e.from.x}
          y1={e.from.y}
          x2={e.to.x}
          y2={e.to.y}
          stroke={edgeStroke(e.type)}
          strokeWidth={1.5}
          strokeDasharray={e.type === "relates_to" ? "4 3" : undefined}
          markerEnd="url(#arrow)"
        />
      ))}
      <defs>
        <marker
          id="arrow"
          viewBox="0 0 10 10"
          refX="10"
          refY="5"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path d="M 0 0 L 10 5 L 0 10 z" fill="#7a8294" />
        </marker>
      </defs>
      {layout.nodes.map((n) => (
        <g
          key={n.id}
          transform={`translate(${n.x}, ${n.y})`}
          onClick={() => focusIssue(n.id)}
          style={{ cursor: "pointer" }}
        >
          <circle
            r={n.id === issueID ? 22 : 16}
            fill={statusColor(n.status)}
            stroke={n.id === issueID ? "#f0a030" : "#1d2230"}
            strokeWidth={n.id === issueID ? 2 : 1}
          />
          <text
            y={-2}
            textAnchor="middle"
            className="fill-text"
            fontSize={9}
            fontFamily="IBM Plex Mono"
          >
            {n.identifier}
          </text>
          <text
            y={9}
            textAnchor="middle"
            className="fill-muted"
            fontSize={8}
          >
            {truncate(n.title, 14)}
          </text>
        </g>
      ))}
    </svg>
  );
}

// ─── layout ────────────────────────────────────────────────

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

function truncate(s: string, max: number): string {
  return s.length > max ? s.slice(0, max - 1) + "…" : s;
}
