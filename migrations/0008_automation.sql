CREATE TABLE IF NOT EXISTS automation_rules (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    team_id      TEXT NOT NULL REFERENCES teams(id),
    name         TEXT NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT true,
    trigger      TEXT NOT NULL,
    conditions   JSONB NOT NULL DEFAULT '[]',
    actions      TEXT[] NOT NULL DEFAULT '{}',
    action_data  JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW()
);

-- Partial index covers the hot path: "active rules for this workspace".
CREATE INDEX IF NOT EXISTS idx_automation_workspace
    ON automation_rules(workspace_id, enabled)
    WHERE enabled = true;

CREATE TABLE IF NOT EXISTS automation_logs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id       TEXT NOT NULL REFERENCES automation_rules(id),
    issue_id      TEXT REFERENCES issues(id),
    trigger       TEXT NOT NULL,
    actions_taken TEXT[] NOT NULL DEFAULT '{}',
    success       BOOLEAN NOT NULL DEFAULT true,
    error         TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_automation_logs_rule
    ON automation_logs(rule_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_automation_logs_issue
    ON automation_logs(issue_id) WHERE issue_id IS NOT NULL;
