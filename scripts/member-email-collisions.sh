#!/usr/bin/env bash
# member-email-collisions.sh — B18.32: list every pair of `members` rows in one workspace whose emails
# differ only by case or surrounding spaces. Such a pair is one person holding two memberships; authz
# now resolves them to ONE (exact spelling first, then the owner row, then the oldest), and nothing
# merges them automatically — this list is what an operator merges by hand, if at all.
#
# Read-only: one READ ONLY transaction, rolled back.
#
#   scripts/member-email-collisions.sh <ssh-target>          e.g. root@<prod-host>
#
# PG_CONTAINER (default talyvor-lens-postgres-1), PG_USER (lens), PG_DB (talyvor_track) override the
# production layout.
set -euo pipefail
target="${1:?usage: scripts/member-email-collisions.sh <ssh-target>}"
container="${PG_CONTAINER:-talyvor-lens-postgres-1}"
user="${PG_USER:-lens}"
db="${PG_DB:-talyvor_track}"

ssh -o ConnectTimeout=10 "$target" \
  "docker exec -i $container psql -U $user -d $db -v ON_ERROR_STOP=1 -At -F ' | '" <<'SQL'
BEGIN READ ONLY;
SELECT count(*) || ' member rows, ' || count(DISTINCT workspace_id) || ' workspaces' FROM members;
SELECT count(*) || ' colliding pair(s)'
  FROM members a JOIN members b
    ON a.workspace_id = b.workspace_id AND a.id < b.id
   AND lower(btrim(a.email)) = lower(btrim(b.email));
SELECT a.workspace_id, a.id, a.role, a.email, b.id, b.role, b.email
  FROM members a JOIN members b
    ON a.workspace_id = b.workspace_id AND a.id < b.id
   AND lower(btrim(a.email)) = lower(btrim(b.email))
 ORDER BY a.workspace_id, lower(btrim(a.email));
ROLLBACK;
SQL
