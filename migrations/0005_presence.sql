-- Presence is in-memory only — the hub's PresenceStore resets when the
-- process restarts. Notifications, however, need to survive restarts so
-- the bell icon in the UI shows what the user missed.

CREATE TABLE IF NOT EXISTS notifications (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    member_id    TEXT NOT NULL REFERENCES members(id),
    type         TEXT NOT NULL,
    title        TEXT NOT NULL,
    body         TEXT NOT NULL DEFAULT '',
    issue_id     TEXT REFERENCES issues(id),
    read         BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

-- Most common query is "unread notifications for this member, newest
-- first". The composite (member_id, read, created_at DESC) serves that
-- exact pattern as an index-only scan.
CREATE INDEX IF NOT EXISTS idx_notifications_member
    ON notifications(member_id, read, created_at DESC);
