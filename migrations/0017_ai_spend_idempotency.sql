-- T7: make AI-cost accumulation idempotent and ledger-backed. ai_spend_events (defined
-- in 0006 but never written) becomes the per-event ledger that issues.ai_cost_usd is
-- the running sum of. event_key is the idempotency key: a re-delivered webhook event,
-- or a repeated syncer reconciliation observing the same total, maps to the same key
-- and is a no-op. The unique index treats a NULL issue_id (orphan spend with no
-- matching issue) as '' so those dedup too.
ALTER TABLE ai_spend_events ADD COLUMN IF NOT EXISTS event_key TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_ai_spend_events_event_key
    ON ai_spend_events (event_key, COALESCE(issue_id, ''));
