-- 0029_instance_secrets.sql — B18.31: one guest-link signing key for every Track process.
-- ADDITIVE ONLY.
--
-- With TRACK_GUEST_SECRET unset, each process used to mint its own random HMAC key, so every guest
-- link broke on restart and a second instance rejected the first one's links. Now the first process
-- to boot without the variable generates a key and stores it here; every process after it — a
-- restart, a second instance — reads the same row (internal/guest/signing_key.go). An operator who
-- sets TRACK_GUEST_SECRET still wins; this row is then unused.
CREATE TABLE IF NOT EXISTS instance_secrets (
    name       TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
