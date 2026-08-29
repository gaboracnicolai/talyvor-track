-- 0027_mark_writerless_columns.sql — W3.66: two columns nothing writes, marked where a DBA
-- with psql will actually see them.
-- COMMENTS ONLY. No table, column, index, constraint or row is created, altered or dropped.
--
-- A census of the shipped schema (30 product tables, 273 columns, read from information_schema
-- after the real migration chain) against every INSERT column list, SET clause, ON CONFLICT
-- assignment and COPY in internal/ and cmd/ found two columns that NO production statement
-- writes. Neither is dead schema a reader can spot: both are SELECTed, scanned and served.
--
--   guests.last_seen_at   nullable, no DEFAULT  -> NULL for every row that has ever existed
--   members.avatar_url    NOT NULL DEFAULT ''   -> the empty string, likewise
--
-- The hazard is not the storage, it is that the next person to write a query sees these in
-- `\d guests` / `\d members` and uses them. talyvor-lens shipped exactly that twice on
-- token_events.cached — once reporting a structural 0 as a measured cache hit rate, once
-- answering estimated_savings_usd = $0.00 for a year — and put the same warning in its own
-- migrations 0114 and 0124 for the same reason.
--
-- The guard that keeps this true is internal/schemaguard/writerless_column_test.go, which
-- re-derives the census on every run rather than trusting this comment: it reds when a new
-- writerless column appears, and reds again when one of these is finally given a writer. This
-- comment is the copy a `\d+` shows; that test is the one CI enforces.
--
-- ⚠ NOT A RECOMMENDATION TO DROP EITHER COLUMN, and not a decision about avatars. Whether
-- Track should acquire an avatar source, or stop shipping the field on the three surfaces that
-- return it, is a product call. This migration records what is measured and takes neither.

COMMENT ON COLUMN guests.last_seen_at IS
    'WRITERLESS as of migration 0027: no production statement sets this column, so it is NULL '
    'for every row. Read but never written. See internal/schemaguard/writerless_column_test.go.';

COMMENT ON COLUMN members.avatar_url IS
    'WRITERLESS as of migration 0027: no production statement sets this column, so it is the '
    'empty-string default for every row, on all three surfaces that return it (member roster '
    'API, MCP list_members, analytics workload). See internal/schemaguard/writerless_column_test.go.';
