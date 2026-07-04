-- 0022_issues_identifier_unique.sql — T8 live-importer, Build C.2: the re-import upsert target.
-- ADDITIVE (a pre-flight guard + a new UNIQUE constraint; no data change).
--
-- WHY this constraint exists:
--   1. issue.Store.UpsertByIdentifier uses INSERT ... ON CONFLICT (workspace_id, identifier) DO UPDATE — which
--      REQUIRES a unique constraint on exactly (workspace_id, identifier). Track had this UNIQUE only on the
--      `teams` table; `issues` carried only UNIQUE(team_id, number). So it is net-new for issues.
--   2. It ALSO closes a latent gap: PR #30's cost attribution resolves `WHERE identifier = $feature` and
--      credits that issue's ai_cost_usd — silently assuming identifier is unambiguous per workspace. Nothing
--      enforced that until now.
--
-- Safe by derivation: Create sets identifier = teamIdentifier || '-' || number, teamIdentifier unique per
-- workspace (teams.UNIQUE(workspace_id, identifier)), number unique per team (issues.UNIQUE(team_id, number))
-- ⇒ (workspace_id, identifier) is already unique for every Create-generated row.
--
-- PRE-FLIGHT SAFETY: on a real deployment where that derivation might have an exception, fail LOUD with a
-- clear message BEFORE touching the table — rather than letting the ALTER fail opaquely. On a clean DB this
-- passes trivially.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM issues GROUP BY workspace_id, identifier HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'duplicate (workspace_id, identifier) rows exist in issues — resolve before adding the unique constraint (Build C.2 upsert + PR #30 WHERE identifier=$feature both require per-workspace identifier uniqueness)';
    END IF;
END $$;

ALTER TABLE issues ADD CONSTRAINT issues_workspace_identifier_key UNIQUE (workspace_id, identifier);
