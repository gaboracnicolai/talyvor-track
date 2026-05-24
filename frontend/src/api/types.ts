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
  // Total tracked seconds across every time entry on the issue.
  // Populated by GetByID; omitted from list payloads.
  time_tracked_sec?: number;
  // template_id is accepted only on Create; the backend reads it out
  // of the request body and merges the template's defaults before
  // the row is inserted. Never returned on reads.
  template_id?: string;
  // Cached score values populated by GetByID — pointers on the
  // backend so we mirror nullable wire shapes here.
  rice_score?: number;
  ice_score?: number;
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

export interface RelationStats {
  total_relations: number;
  blocking_chains: number;
  blocked_issues: number;
  duplicate_pairs: number;
}

export interface BlockingIssue extends Issue {
  blocks_count: number;
  blocked_issue_ids: string[];
}

// ─── Roadmap ────────────────────────────────────────────
// Mirrors internal/project/roadmap.go.

export interface RoadmapMilestone {
  id: string;
  workspace_id: string;
  project_id: string;
  name: string;
  description: string;
  status: string;
  target_date?: string;
  completed_at?: string;
  created_at: string;
  updated_at: string;
  issue_count: number;
  completed_count: number;
  ai_cost_usd: number;
}

export interface RoadmapProject {
  id: string;
  workspace_id: string;
  team_id: string;
  team_name: string;
  name: string;
  identifier: string;
  description: string;
  status: string;
  priority: number;
  start_date?: string;
  target_date?: string;
  created_at: string;
  updated_at: string;
  milestones: RoadmapMilestone[];
  issue_count: number;
  completed_count: number;
  completion_pct: number;
  ai_cost_usd: number;
}

export interface RoadmapResponse {
  projects: RoadmapProject[];
  date_range: { start: string; end: string };
}

// ─── Time tracking ──────────────────────────────────────
// Mirrors internal/timetracking/store.go.

export interface TimeEntryRecord {
  id: string;
  issue_id: string;
  workspace_id: string;
  member_id: string;
  description: string;
  started_at: string;
  stopped_at?: string;
  duration_sec: number;
  billable: boolean;
  created_at: string;
}

export interface TimerState {
  running: boolean;
  issue_id?: string;
  started_at?: string;
  elapsed_sec: number;
}

export interface TimeSummary {
  issue_id: string;
  total_sec: number;
  billable_sec: number;
  member_count: number;
  entry_count: number;
}

export interface MemberTime {
  member_id: string;
  name: string;
  total_sec: number;
  billable_sec: number;
}

export interface ProjectTime {
  project_id: string;
  name: string;
  total_sec: number;
  billable_sec: number;
}

export interface WorkspaceTimeSummary {
  total_sec: number;
  billable_sec: number;
  by_member: MemberTime[];
  by_project: ProjectTime[];
}

// ─── Issue templates ────────────────────────────────────
// Mirrors internal/template/store.go.

export interface IssueTemplate {
  id: string;
  workspace_id: string;
  team_id?: string;
  name: string;
  description: string;
  icon: string;
  title_format: string;
  body: string;
  default_status: string;
  default_priority: number;
  default_labels: string[];
  default_assignee?: string;
  field_defaults: Record<string, string>;
  created_at: string;
  updated_at: string;
}

// ─── Guests ─────────────────────────────────────────────
// Mirrors internal/guest/store.go.

export type GuestRole = "viewer" | "commenter" | "editor";

export interface GuestRecord {
  id: string;
  workspace_id: string;
  project_id?: string;
  email: string;
  name: string;
  role: GuestRole;
  active: boolean;
  created_at: string;
  last_seen_at?: string;
}

export interface InviteDetail {
  workspace_id: string;
  project_id?: string;
  email: string;
  role: GuestRole;
  expires_at: string;
  invited_by: string;
}

export interface InviteCreateResponse {
  invite_url: string;
  expires_at: string;
  role: GuestRole;
}

export interface AcceptInviteResponse {
  guest_id: string;
  workspace_id: string;
  project_id?: string;
  role: GuestRole;
  access_token: string;
}

// ─── Scoring (RICE / ICE) ────────────────────────────────
// Mirrors internal/scoring/store.go.

export type ScoringMethod = "rice" | "ice";

export interface RICEScore {
  reach: number;
  impact: number;
  confidence: number;
  effort: number;
  score: number;
}

export interface ICEScore {
  impact: number;
  confidence: number;
  ease: number;
  score: number;
}

export interface IssueScoreRecord {
  id: string;
  issue_id: string;
  workspace_id: string;
  method: ScoringMethod;
  rice?: RICEScore;
  ice?: ICEScore;
  notes: string;
  scored_by: string;
  created_at: string;
  updated_at: string;
}

export interface ScoredIssue extends Issue {
  score: number;
  scoring_method: ScoringMethod;
  score_rank: number;
}

export interface ScoreSummary {
  total_scored: number;
  total_issues: number;
  coverage_pct: number;
  avg_rice_score: number;
  avg_ice_score: number;
  top_issue_id: string;
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
