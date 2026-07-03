-- 0019_ai_spend_request_id.sql — T7 follow-up (Build 2): per-request cost attribution.
-- ADDITIVE ONLY. Adds the per-request exactly-once key the new syncer accumulator uses. The legacy
-- (event_key, COALESCE(issue_id,'')) unique index stays in place for the dead webhook path (RecordSpendEvent) —
-- no drops, no key swap.
--
-- request_id is the Lens per-request identifier (GET /v1/api/spend/by-request). The partial unique index makes
-- the syncer's INSERT ... ON CONFLICT (request_id) DO NOTHING land each request's cost EXACTLY ONCE, however
-- many times the last-24h window is re-pulled (~96×/day). The WHERE request_id <> '' clause excludes the
-- legacy rows (dead webhook path writes no request_id), so it constrains only new per-request rows.
ALTER TABLE ai_spend_events ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_ai_spend_events_request
    ON ai_spend_events (request_id) WHERE request_id <> '';
