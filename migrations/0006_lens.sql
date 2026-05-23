-- ai_cost_usd and ai_tokens already exist on issues from Phase 1. This
-- migration adds the spend-event log used to track WHEN cost arrived
-- (via 15-minute sync or a real-time webhook from Lens).

CREATE TABLE IF NOT EXISTS ai_spend_events (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    issue_id     TEXT REFERENCES issues(id),
    lens_feature TEXT NOT NULL DEFAULT '',
    cost_usd     FLOAT NOT NULL DEFAULT 0,
    tokens       INTEGER NOT NULL DEFAULT 0,
    source       TEXT NOT NULL DEFAULT 'sync',
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_spend_events_workspace
    ON ai_spend_events(workspace_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_spend_events_issue
    ON ai_spend_events(issue_id) WHERE issue_id IS NOT NULL;
