-- 0021_workspace_integrations.sql — T8 live-importer, Build C.1: the per-workspace provider credential store.
-- ADDITIVE. Holds a workspace's Linear/Jira API token so an *_api import job can run off-request (Build B
-- runner, Build C.3). SECURITY-SENSITIVE — it stores LIVE customer API tokens.
--
-- THE TOKEN IS NEVER STORED IN PLAINTEXT. token_ciphertext is AES-256-GCM output produced in Go before
-- insert; token_nonce is the per-encryption random GCM nonce. There is NO plaintext token column — by design,
-- so a DB dump / replica / backup never contains a usable credential. workspace_id is the tenancy anchor
-- (FK); project_or_team_key/base_url are non-secret provider config. UNIQUE(workspace_id, provider): one
-- integration per provider per workspace (re-configuring UPDATEs it).
CREATE TABLE IF NOT EXISTS workspace_integrations (
    id                  TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id),  -- tenancy anchor
    provider            TEXT NOT NULL,                            -- 'linear' | 'jira'
    token_ciphertext    BYTEA NOT NULL,                           -- AES-256-GCM ciphertext (NEVER plaintext)
    token_nonce         BYTEA NOT NULL,                           -- the per-encryption GCM nonce (random)
    project_or_team_key TEXT NOT NULL DEFAULT '',                 -- Linear team id / Jira project key (non-secret)
    base_url            TEXT NOT NULL DEFAULT '',                 -- Jira site URL; Linear fixed (stored for symmetry)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (workspace_id, provider)
);
