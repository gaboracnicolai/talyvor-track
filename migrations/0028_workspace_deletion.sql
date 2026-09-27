-- 0028_workspace_deletion.sql — B18.30: deleting a workspace is recoverable for 14 days, then final.
-- ADDITIVE ONLY.
--
-- DELETE /v1/workspaces/{wsID} used to issue `DELETE FROM workspaces` directly, which the child tables'
-- foreign keys refuse (members, teams, issues, import_jobs … all REFERENCE workspaces(id) with no ON
-- DELETE clause), so an owner's delete answered 500 and nothing happened. Now the delete only stamps
-- deleted_at: the workspace stops answering every route except restore, and the owner can restore it
-- intact for 14 days. After that the purge sweep removes the workspace and everything it owns, children
-- first (internal/workspace/purge.go), without changing any foreign key's ON DELETE action.
ALTER TABLE workspaces
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

-- The purge sweep scans only deleted workspaces.
CREATE INDEX IF NOT EXISTS idx_workspaces_deleted_at
    ON workspaces (deleted_at) WHERE deleted_at IS NOT NULL;
