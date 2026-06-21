-- 0018_members_email_index.sql
-- T10 puts an email->memberships lookup (SELECT ... FROM members WHERE email=$1) in the
-- hot path of EVERY authenticated /v1 request: the gateway-verified user is resolved to
-- their authorized workspace(s) per call. The existing UNIQUE(workspace_id,email) is a
-- composite with email as the SECOND column, so by the leftmost-prefix rule it does NOT
-- serve a bare WHERE email= seek (verified: EXPLAIN shows a Seq Scan without this index,
-- an Index Scan with it). Add a dedicated email index. Pure index add — re-runnable.
CREATE INDEX IF NOT EXISTS idx_members_email ON members(email);
