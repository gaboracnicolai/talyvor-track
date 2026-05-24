-- RICE / ICE scoring for product prioritisation.
--
-- One row per issue (UNIQUE on issue_id) — re-scoring uses ON CONFLICT
-- DO UPDATE so the row id stays stable while the values rotate. We
-- store BOTH the input components and the computed score; the score
-- is denormalised so the prioritised-backlog query can ORDER BY it
-- directly without recomputing per request.

CREATE TABLE IF NOT EXISTS issue_scores (
    id              TEXT PRIMARY KEY DEFAULT gen_random_uuid()::text,
    issue_id        TEXT UNIQUE NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    method          TEXT NOT NULL,
    rice_reach      FLOAT,
    rice_impact     FLOAT,
    rice_confidence FLOAT,
    rice_effort     FLOAT,
    rice_score      FLOAT,
    ice_impact      FLOAT,
    ice_confidence  FLOAT,
    ice_ease        FLOAT,
    ice_score       FLOAT,
    notes           TEXT NOT NULL DEFAULT '',
    scored_by       TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ DEFAULT NOW(),
    updated_at      TIMESTAMPTZ DEFAULT NOW()
);

-- Partial indexes on each scoring method — the prioritised-backlog
-- query orders by one of these columns and skips rows where it's
-- NULL. NULLS LAST keeps unscored rows at the tail.
CREATE INDEX IF NOT EXISTS idx_scores_workspace_rice
    ON issue_scores(workspace_id, rice_score DESC NULLS LAST)
    WHERE rice_score IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_scores_workspace_ice
    ON issue_scores(workspace_id, ice_score DESC NULLS LAST)
    WHERE ice_score IS NOT NULL;
