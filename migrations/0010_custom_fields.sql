-- Custom fields: per-workspace (and optionally per-team) extensions to
-- the issue schema. Each field has a type ("text", "number", "select",
-- etc.) that drives both render and validation. Values live in a
-- separate (issue_id, field_id) row so adding a new field never
-- touches the issues table.

CREATE TABLE IF NOT EXISTS custom_fields (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    team_id      TEXT REFERENCES teams(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    type         TEXT NOT NULL,
    options      TEXT[] NOT NULL DEFAULT '{}',
    required     BOOLEAN NOT NULL DEFAULT false,
    position     INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (workspace_id, name)
);

CREATE TABLE IF NOT EXISTS issue_field_values (
    issue_id   TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    field_id   TEXT NOT NULL REFERENCES custom_fields(id) ON DELETE CASCADE,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (issue_id, field_id)
);

CREATE INDEX IF NOT EXISTS idx_field_values_issue
    ON issue_field_values(issue_id);
CREATE INDEX IF NOT EXISTS idx_field_values_field
    ON issue_field_values(field_id);

-- Workspace-wide fields (team_id IS NULL) get listed alongside the
-- team-scoped ones; this partial index speeds the common "list all
-- fields for team X" query without bloating the primary index.
CREATE INDEX IF NOT EXISTS idx_custom_fields_workspace_position
    ON custom_fields(workspace_id, position);
