-- Guest access: external collaborators with scoped read / comment /
-- edit permissions. Two tables — invites (pending) and guests
-- (accepted) — so a revoked guest doesn't reopen via a stale token.
--
-- Guest access tokens are stateless (signed HMAC-SHA256 in app code,
-- see internal/guest/store.go). The guests table is the source of
-- truth for who has access; the tokens just prove identity.

CREATE TABLE IF NOT EXISTS guest_invites (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   TEXT REFERENCES projects(id) ON DELETE CASCADE,
    email        TEXT NOT NULL,
    role         TEXT NOT NULL DEFAULT 'viewer',
    token        TEXT UNIQUE NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    accepted_at  TIMESTAMPTZ,
    invited_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS guests (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id   TEXT REFERENCES projects(id) ON DELETE CASCADE,
    email        TEXT NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT 'viewer',
    active       BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ,
    UNIQUE (workspace_id, email)
);

-- Partial index for the open-invite lookup path. Once an invite is
-- accepted we never look it up by token again, so a partial index
-- keeps the hot path lean.
CREATE INDEX IF NOT EXISTS idx_guest_invites_token
    ON guest_invites(token) WHERE accepted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_guests_workspace
    ON guests(workspace_id, active);
