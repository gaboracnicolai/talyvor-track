-- Public feature boards: user-facing portals where customers (and
-- anonymous visitors) can submit feature requests and vote. Three
-- tables:
--   - feature_boards   (one workspace, many boards)
--   - feature_posts    (ideas; vote_count denormalised)
--   - feature_votes    (1 row per (post, email))
--
-- Vote dedupe lives in the UNIQUE(post_id, email) constraint. The
-- denormalised vote_count on feature_posts is the read-time column
-- the prioritised list sorts by, refreshed inside the Vote/Unvote
-- transactions.

CREATE TABLE IF NOT EXISTS feature_boards (
    id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    slug            TEXT NOT NULL,
    public          BOOLEAN NOT NULL DEFAULT true,
    allow_anonymous BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (workspace_id, slug)
);

CREATE TABLE IF NOT EXISTS feature_posts (
    id           TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    board_id     TEXT NOT NULL REFERENCES feature_boards(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'open',
    vote_count   INTEGER NOT NULL DEFAULT 0,
    issue_id     TEXT REFERENCES issues(id) ON DELETE SET NULL,
    author_name  TEXT NOT NULL DEFAULT 'Anonymous',
    author_email TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_posts_board_votes
    ON feature_posts(board_id, vote_count DESC);
CREATE INDEX IF NOT EXISTS idx_posts_board_status
    ON feature_posts(board_id, status);

CREATE TABLE IF NOT EXISTS feature_votes (
    id         TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    post_id    TEXT NOT NULL REFERENCES feature_posts(id) ON DELETE CASCADE,
    email      TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (post_id, email)
);

CREATE INDEX IF NOT EXISTS idx_votes_post ON feature_votes(post_id);
