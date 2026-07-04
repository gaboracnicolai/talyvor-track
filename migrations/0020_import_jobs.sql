-- 0020_import_jobs.sql — T8 live-importer, Build B: the async import-job spine.
-- ADDITIVE ONLY. A durable import_jobs record so a bulk import runs OFF-request (the inline
-- middleware.Timeout(30s) can't hold a real Linear/Jira import) and its partial/terminal state survives the
-- request. The workspace_id captured here — the server-resolved membership workspace, written at creation
-- under the authz gate — is the ONLY workspace a job can ever write to; the runner reads it from this row.

CREATE TABLE IF NOT EXISTS import_jobs (
    id            TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id),  -- the authorized workspace, captured at creation
    team_id       TEXT NOT NULL,
    -- source_type is one self-describing {provider}_{transport} field. Closed set of 4:
    --   'linear_csv', 'jira_csv'  (Build B — a CSV upload; has an import_job_payloads row)
    --   'linear_api', 'jira_api'  (Build C — a provider API pull; NO payload row)
    -- The runner switches on it to pick (source constructor, row mapper); an unknown value fails the job
    -- cleanly (status=failed + error_summary), never panics.
    source_type   TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',          -- pending | running | succeeded | partial | failed
    imported      INTEGER NOT NULL DEFAULT 0,
    skipped       INTEGER NOT NULL DEFAULT 0,
    failed        INTEGER NOT NULL DEFAULT 0,
    error_summary TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ
);

-- The poller claims pending jobs oldest-first with FOR UPDATE SKIP LOCKED — this index keeps that cheap.
CREATE INDEX IF NOT EXISTS idx_import_jobs_pending
    ON import_jobs (created_at) WHERE status = 'pending';
-- Status reads are scoped to a workspace (a caller only sees their own jobs).
CREATE INDEX IF NOT EXISTS idx_import_jobs_workspace
    ON import_jobs (workspace_id, created_at DESC);

-- The source payload (the uploaded CSV bytes) lives in a SEPARATE table so the frequently-read/-polled
-- import_jobs status row stays small — a 64 MiB blob never drags a status poll. Nullable-by-absence: an API
-- source (Build C) creates no payload row. ON DELETE CASCADE ties the blob's lifetime to the job.
CREATE TABLE IF NOT EXISTS import_job_payloads (
    job_id     TEXT PRIMARY KEY REFERENCES import_jobs(id) ON DELETE CASCADE,
    payload    BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
