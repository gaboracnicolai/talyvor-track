-- Time tracking: per-issue, per-member entries with start/stop
-- timestamps and a denormalised duration_sec for fast summary
-- aggregations.
--
-- The partial index on (member_id, workspace_id) WHERE stopped_at IS
-- NULL is what makes "is there a running timer for this member?" a
-- single B-tree lookup — it's read on every IssueDetail mount.
--
-- ON DELETE CASCADE on issue_id only fires on hard DELETE; issue
-- soft-deletes (status='cancelled') leave entries intact for
-- historical billing.

CREATE TABLE IF NOT EXISTS time_entries (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    issue_id     TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    member_id    TEXT NOT NULL REFERENCES members(id),
    description  TEXT NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL,
    stopped_at   TIMESTAMPTZ,
    duration_sec INTEGER NOT NULL DEFAULT 0,
    billable     BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_time_entries_issue
    ON time_entries(issue_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_time_entries_member
    ON time_entries(member_id, workspace_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_time_entries_running
    ON time_entries(member_id, workspace_id)
    WHERE stopped_at IS NULL;
