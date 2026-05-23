// Shared API types. Mirror the Go server's JSON shapes so the
// TypeScript compiler catches drift between client and server at
// build time. Field names match the JSON tags exactly.

export type IssueStatus =
  | "backlog"
  | "todo"
  | "in_progress"
  | "in_review"
  | "done"
  | "cancelled";

export type IssuePriority = 0 | 1 | 2 | 3 | 4;

export interface Issue {
  id: string;
  workspace_id: string;
  team_id: string;
  project_id?: string;
  number: number;
  identifier: string;
  title: string;
  description: string;
  status: IssueStatus;
  priority: IssuePriority;
  assignee_id?: string;
  creator_id: string;
  cycle_id?: string;
  parent_id?: string;
  due_date?: string;
  completed_at?: string;
  lens_feature: string;
  ai_cost_usd: number;
  ai_tokens: number;
  labels: string[];
  sort_order: number;
  created_at: string;
  updated_at: string;
  // Custom-field values keyed by field ID. Optional because the
  // backend omits the key when empty.
  field_values?: Record<string, string>;
  // True when the backend determined the issue has at least one
  // open blocker. Populated on detail reads; omitted from list rows
  // to avoid the per-row blocked check on bulk fetches.
  is_blocked?: boolean;
}

// ─── Relations / dependencies ───────────────────────────
// Mirrors internal/dependency/store.go's RelationType union.

export type RelationType =
  | "blocks"
  | "blocked_by"
  | "relates_to"
  | "duplicates"
  | "clones";

export interface Relation {
  id: string;
  source_id: string;
  target_id: string;
  type: RelationType;
  workspace_id: string;
  created_by: string;
  created_at: string;
}

export interface RelationWithIssue extends Relation {
  issue: Issue;
}

export interface GraphNode {
  id: string;
  identifier: string;
  title: string;
  status: string;
  is_blocked?: boolean;
}

export interface GraphEdge {
  source: string;
  target: string;
  type: RelationType;
}

export interface DependencyGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export interface Team {
  id: string;
  workspace_id: string;
  name: string;
  identifier: string;
  color: string;
  icon: string;
  created_at: string;
  updated_at: string;
}

export interface Project {
  id: string;
  workspace_id: string;
  team_id: string;
  name: string;
  identifier: string;
  description: string;
  status: string;
  priority: number;
  start_date?: string;
  target_date?: string;
}

export interface CycleVelocity {
  cycle_id: string;
  cycle_name: string;
  start_date: string;
  end_date: string;
  completed: number;
  total: number;
  completion_rate: number;
  ai_cost_usd: number;
}

export interface BurndownPoint {
  date: string;
  remaining: number;
  ideal: number;
}

export interface BurndownReport {
  cycle_id: string;
  cycle_name: string;
  start_date: string;
  end_date: string;
  points: BurndownPoint[];
  is_on_track: boolean;
  projected_end?: string;
}

export interface DailyCost {
  date: string;
  cost_usd: number;
  issues_worked: number;
}

export interface AICostTrends {
  total_cost_usd: number;
  daily_costs: DailyCost[];
  top_cost_issues: Array<{
    issue_id: string;
    identifier: string;
    title: string;
    cost_usd: number;
    tokens: number;
  }>;
  cost_by_team: Array<{ team_id: string; name: string; cost_usd: number }>;
  cost_by_label: Array<{ label: string; cost_usd: number }>;
  projected_monthly_usd: number;
  avg_cost_per_issue: number;
}

export interface MemberWorkload {
  member_id: string;
  name: string;
  avatar_url: string;
  open_issues: number;
  in_progress: number;
  overdue: number;
  ai_cost_usd: number;
}

// ─── Custom fields ──────────────────────────────────────
// Mirrors internal/customfield/store.go's CustomField struct. Values
// flow as `string` on the wire; multi-select is JSON-encoded
// `"[\"a\",\"b\"]"` so the column type stays uniform.

export type CustomFieldType =
  | "text"
  | "number"
  | "date"
  | "select"
  | "multi"
  | "url"
  | "member"
  | "checkbox";

export interface CustomField {
  id: string;
  workspace_id: string;
  team_id?: string;
  name: string;
  type: CustomFieldType;
  options: string[];
  required: boolean;
  position: number;
  created_at: string;
}

// Event shape that flows over the WebSocket.
export interface RealtimeEvent {
  type: string;
  workspace_id: string;
  room_id: string;
  actor_id: string;
  payload: unknown;
  timestamp: string;
}
