-- Issue relations: typed directed edges between issues. blocks /
-- blocked_by / duplicates are stored bidirectionally (the store
-- inserts both rows on create); relates_to and clones are stored as
-- a single directed row.

CREATE TABLE IF NOT EXISTS issue_relations (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    source_id    TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    target_id    TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    type         TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (source_id, target_id, type)
);

CREATE INDEX IF NOT EXISTS idx_relations_source    ON issue_relations(source_id);
CREATE INDEX IF NOT EXISTS idx_relations_target    ON issue_relations(target_id);
CREATE INDEX IF NOT EXISTS idx_relations_workspace ON issue_relations(workspace_id);
