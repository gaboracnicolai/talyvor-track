CREATE TABLE IF NOT EXISTS milestones (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id   TEXT NOT NULL REFERENCES projects(id),
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'upcoming',
    target_date  TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_milestones_project
    ON milestones(project_id, status);

ALTER TABLE issues
    ADD COLUMN IF NOT EXISTS milestone_id TEXT REFERENCES milestones(id);

CREATE INDEX IF NOT EXISTS idx_issues_milestone
    ON issues(milestone_id) WHERE milestone_id IS NOT NULL;
