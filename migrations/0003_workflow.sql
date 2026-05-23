CREATE TABLE IF NOT EXISTS workflow_statuses (
    id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    team_id    TEXT NOT NULL REFERENCES teams(id),
    name       TEXT NOT NULL,
    color      TEXT NOT NULL DEFAULT '#94a3b8',
    category   TEXT NOT NULL DEFAULT 'unstarted',
    position   INTEGER NOT NULL DEFAULT 0,
    is_default BOOLEAN NOT NULL DEFAULT false,
    UNIQUE(team_id, name)
);

CREATE INDEX IF NOT EXISTS idx_workflow_statuses_team
    ON workflow_statuses(team_id, position);

CREATE TABLE IF NOT EXISTS labels (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    team_id      TEXT REFERENCES teams(id),
    name         TEXT NOT NULL,
    color        TEXT NOT NULL DEFAULT '#94a3b8',
    description  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(workspace_id, name)
);

CREATE INDEX IF NOT EXISTS idx_labels_workspace
    ON labels(workspace_id);

CREATE INDEX IF NOT EXISTS idx_labels_team
    ON labels(team_id) WHERE team_id IS NOT NULL;
