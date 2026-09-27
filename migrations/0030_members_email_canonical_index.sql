-- 0030_members_email_canonical_index.sql — B18.32: member emails match regardless of case.
-- ADDITIVE ONLY.
--
-- authz now matches members.email lower-cased and trimmed (internal/authz/resolver.go), and
-- member.AddMember refuses a second spelling of an address the workspace already has. The stored
-- address is kept exactly as typed, so no row is rewritten and nobody's sign-in changes. This is a
-- plain index, NOT a unique one: rows that already collide (same workspace, same address in a
-- different case) are listed by scripts/member-email-collisions.sh and merged by nobody
-- automatically — a unique index would refuse to build on a database holding one.
CREATE INDEX IF NOT EXISTS idx_members_email_canonical ON members (lower(btrim(email)));
