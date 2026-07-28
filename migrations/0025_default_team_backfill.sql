-- 0025_default_team_backfill.sql
--
-- Every workspace must have at least one team, or it cannot take a single issue:
-- issues.team_id is NOT NULL REFERENCES teams(id) (0002_issues.sql), and the team's
-- identifier is the namespace of every issue's human key ("<identifier>-<number>"). Until
-- this release nothing created a team — workspace.CreateWithOwner made the workspace and
-- its owner member and stopped — so every create-issue in a fresh workspace answered
-- CREATE_FAILED. The write path was dead for every new user on every deployment.
--
-- CreateWithOwner now seeds the default team in the same transaction as the owner member.
-- That fixes every workspace created FROM NOW ON and does nothing for the ones already out
-- there, which are precisely the workspaces belonging to the people who hit the bug.
--
-- ⚠ WHY THIS BACKFILLS WHERE 0024 REFUSED TO. 0024 found zero-owner workspaces and failed
-- loud rather than promoting someone, because choosing which member becomes owner is a
-- security decision a migration has no standing to make. Nothing comparable is being
-- decided here: the team created is brand new and empty, it grants nobody anything, and it
-- is byte-for-byte the team CreateWithOwner now creates for everyone else. The alternative
-- — failing loud — would demand a human hand-create a team for every existing workspace
-- before Track could be started at all, which is a worse answer to a question with an
-- unambiguous default.
--
-- Scoped to workspaces with NO team whatsoever, so this can neither collide with an
-- existing 'GEN' identifier (UNIQUE(workspace_id, identifier)) nor add a second team to a
-- workspace that already made its own choices. Idempotent by the same condition: a re-run
-- matches nothing.
INSERT INTO teams (workspace_id, name, identifier)
SELECT w.id, 'General', 'GEN'
FROM workspaces w
WHERE NOT EXISTS (
    SELECT 1 FROM teams t WHERE t.workspace_id = w.id
);

-- The six built-in workflow statuses, so a backfilled team is the same team a hand-created
-- one is (team.Handler.Create seeds these via workflow.Engine.SeedDefaults). Restricted to
-- teams that have none, so this touches only what the INSERT above just created and stays
-- idempotent on a re-run.
--
-- ⚠ THIS LIST IS DUPLICATED from workflow.Engine.SeedDefaults, and that is a second source
-- of truth — normally the thing to avoid. It is accepted here because a migration is a
-- HISTORICAL RECORD, not a live rule: it must keep meaning what it meant on the day it ran,
-- so it cannot call Go that will change underneath it. The `category` strings were checked
-- against internal/workflow/engine.go:30-34 rather than assumed — workflow_statuses.category
-- carries no CHECK constraint, so a wrong value here would have been stored silently, which
-- is the exact failure shape this whole change exists to remove.
INSERT INTO workflow_statuses (team_id, name, color, category, position, is_default)
SELECT t.id, d.name, d.color, d.category, d.position, TRUE
FROM teams t
CROSS JOIN (VALUES
    ('Backlog',     '#94a3b8', 'backlog',   0),
    ('Todo',        '#94a3b8', 'unstarted', 1),
    ('In Progress', '#3b82f6', 'started',   2),
    ('In Review',   '#f59e0b', 'started',   3),
    ('Done',        '#10b981', 'completed', 4),
    ('Cancelled',   '#ef4444', 'cancelled', 5)
) AS d(name, color, category, position)
WHERE NOT EXISTS (
    SELECT 1 FROM workflow_statuses ws WHERE ws.team_id = t.id
)
ON CONFLICT (team_id, name) DO NOTHING;
