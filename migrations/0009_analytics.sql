-- Analytics queries read from existing tables; no new state. These
-- composite indexes serve the three hot query shapes the engine runs:
--   1. "completed issues in this team+window with their cost"
--   2. "top-cost issues for this workspace"
--   3. "overdue issues per assignee"

CREATE INDEX IF NOT EXISTS idx_issues_analytics
    ON issues(workspace_id, team_id, status, created_at, completed_at, ai_cost_usd)
    WHERE status IN ('done', 'cancelled');

CREATE INDEX IF NOT EXISTS idx_issues_ai_cost
    ON issues(workspace_id, ai_cost_usd DESC)
    WHERE ai_cost_usd > 0;

CREATE INDEX IF NOT EXISTS idx_issues_due
    ON issues(workspace_id, due_date)
    WHERE due_date IS NOT NULL AND status NOT IN ('done', 'cancelled');
