-- 0022_issues_identifier_unique.sql — T8 live-importer, Build C.2: the re-import upsert target.
-- ADDITIVE. issue.Store.UpsertByIdentifier lands a provider issue on its identifier (the provider-key) with
-- INSERT ... ON CONFLICT (workspace_id, identifier) DO UPDATE — which requires a UNIQUE constraint/index on
-- exactly those columns. Track had UNIQUE(workspace_id, identifier) only on `teams`, and issues carried only
-- UNIQUE(team_id, number) — so this index is net-new.
--
-- SAFE on existing data: Create sets identifier = teamIdentifier || '-' || number, where teamIdentifier is
-- unique per workspace (teams.UNIQUE(workspace_id, identifier)) and number is unique per team
-- (issues.UNIQUE(team_id, number)) — so (workspace_id, identifier) is already unique for every existing row.
-- The index also makes PR #30's cost attribution (WHERE identifier = $feature) unambiguous per workspace.
CREATE UNIQUE INDEX IF NOT EXISTS issues_workspace_identifier_key
    ON issues (workspace_id, identifier);
