-- 0017_notification_preferences.sql
--
-- Per-member, per-event email opt-out for the notification system.
--
-- Opt-out model: the ABSENCE of a row means "send email for this event".
-- A row with email_enabled=false suppresses that event for the member. This
-- keeps notifications on by default while letting members turn off individual
-- event types.
--
-- Note: members.email already exists (migrations/0001_core.sql, NOT NULL), so
-- no email column is added here.

CREATE TABLE IF NOT EXISTS notification_preferences (
    member_id     TEXT    NOT NULL REFERENCES members(id),
    event_type    TEXT    NOT NULL,
    email_enabled BOOLEAN NOT NULL DEFAULT true,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (member_id, event_type)
);
