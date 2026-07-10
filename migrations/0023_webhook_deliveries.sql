-- SEC-7: cross-delivery webhook replay guard. One row per (source, delivery_id). The handler claims a
-- delivery exactly once (INSERT ON CONFLICT DO NOTHING) and no-ops repeats — GitHub retries and attacker
-- re-POSTs of an identically-signed body stop re-running side effects. received_at supports bounded
-- retention (periodic prune) + a freshness window. Delivery ids are provider-global, so this is NOT
-- workspace-scoped (deliberately — no workspace_id column; nosemgrep-exempt by shape).
CREATE TABLE IF NOT EXISTS webhook_deliveries (
    source       TEXT        NOT NULL,
    delivery_id  TEXT        NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source, delivery_id)
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_received_at ON webhook_deliveries (received_at);
