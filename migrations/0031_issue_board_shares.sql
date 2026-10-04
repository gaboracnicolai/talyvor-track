-- 0031_issue_board_shares.sql — B27.30: a read-only issue board anyone with the link can open.
-- ADDITIVE ONLY.
--
-- A workspace owner turns a link on (optionally for one project) and off again. The token is the
-- whole credential: GET /v1/public/issue-boards/{token} answers the board's issues — identifier,
-- title, status, priority — and nothing else, while the link is not revoked and the workspace is
-- not deleted. The token is kept as written so a member can copy the link again later.
-- Both references CASCADE: a link goes with its workspace (the purge) and with its project.
CREATE TABLE IF NOT EXISTS issue_board_shares (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   TEXT REFERENCES projects(id) ON DELETE CASCADE,
    token        TEXT NOT NULL UNIQUE,
    created_by   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_issue_board_shares_workspace
    ON issue_board_shares (workspace_id) WHERE revoked_at IS NULL;
