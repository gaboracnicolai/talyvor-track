CREATE TABLE IF NOT EXISTS cycles (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    team_id      TEXT NOT NULL REFERENCES teams(id),
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    name         TEXT NOT NULL,
    number       INTEGER NOT NULL,
    status       TEXT NOT NULL DEFAULT 'upcoming',
    start_date   TIMESTAMPTZ NOT NULL,
    end_date     TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(team_id, number)
);

CREATE SEQUENCE IF NOT EXISTS issue_number_seq;

CREATE TABLE IF NOT EXISTS issues (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    team_id      TEXT NOT NULL REFERENCES teams(id),
    project_id   TEXT REFERENCES projects(id),
    number       INTEGER NOT NULL,
    identifier   TEXT NOT NULL,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'backlog',
    priority     INTEGER NOT NULL DEFAULT 0,
    assignee_id  TEXT REFERENCES members(id),
    creator_id   TEXT NOT NULL,
    cycle_id     TEXT REFERENCES cycles(id),
    parent_id    TEXT REFERENCES issues(id),
    due_date     TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    lens_feature TEXT NOT NULL DEFAULT '',
    ai_cost_usd  FLOAT NOT NULL DEFAULT 0,
    ai_tokens    INTEGER NOT NULL DEFAULT 0,
    labels       TEXT[] NOT NULL DEFAULT '{}',
    sort_order   FLOAT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(team_id, number)
);

CREATE INDEX IF NOT EXISTS idx_issues_workspace ON issues(workspace_id, status);
CREATE INDEX IF NOT EXISTS idx_issues_team ON issues(team_id, status, priority);
CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(assignee_id) WHERE assignee_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_issues_cycle ON issues(cycle_id) WHERE cycle_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_issues_search ON issues USING gin(
    to_tsvector('english', title || ' ' || description)
);
CREATE INDEX IF NOT EXISTS idx_issues_labels ON issues USING gin(labels);

CREATE TABLE IF NOT EXISTS comments (
    id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    issue_id   TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author_id  TEXT NOT NULL,
    body       TEXT NOT NULL,
    edited_at  TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_comments_issue ON comments(issue_id, created_at);
