-- Issue templates: pre-filled issue forms for common workflows
-- (bug reports, incidents, feature requests). Body is plain markdown
-- rendered live in the new-issue dialog. JSONB field_defaults lets a
-- template seed custom-field values without baking a separate table.
--
-- UNIQUE(workspace_id, name) backs SeedDefaults's idempotent INSERT —
-- re-running the seed on a workspace that already has them is a no-op.

CREATE TABLE IF NOT EXISTS issue_templates (
    id               TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    team_id          TEXT REFERENCES teams(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    icon             TEXT NOT NULL DEFAULT '📋',
    title_format     TEXT NOT NULL DEFAULT '',
    body             TEXT NOT NULL DEFAULT '',
    default_status   TEXT NOT NULL DEFAULT 'backlog',
    default_priority INTEGER NOT NULL DEFAULT 3,
    default_labels   TEXT[] NOT NULL DEFAULT '{}',
    default_assignee TEXT REFERENCES members(id) ON DELETE SET NULL,
    field_defaults   JSONB NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ DEFAULT NOW(),
    updated_at       TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (workspace_id, name)
);

CREATE INDEX IF NOT EXISTS idx_templates_workspace
    ON issue_templates(workspace_id, team_id NULLS FIRST);
