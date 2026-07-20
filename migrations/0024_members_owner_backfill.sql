-- 0024_members_owner_backfill.sql
--
-- Multi-member roles land with an invariant: every workspace must retain at least one
-- OWNER, or the owner-gated operations (delete/administer the workspace, write
-- integration secrets, member management) would lock everyone out of their own workspace.
--
-- Today the ONLY member-insert is workspace.CreateWithOwner, which hard-codes role
-- 'owner', so every existing workspace already has an owner and this migration is a
-- NO-OP. It deliberately does NOT guess a fix for a zero-owner workspace (which could
-- only arise from out-of-band writes): rather than silently promoting an arbitrary
-- member — a security decision that isn't the migration's to make — it FAILS LOUD so a
-- human assigns the owner and re-runs. Runs inside the migrate runner's per-file
-- transaction, so a RAISE rolls the whole apply back cleanly.
DO $$
DECLARE
    zero_owner_count integer;
BEGIN
    SELECT count(*) INTO zero_owner_count
    FROM workspaces w
    WHERE NOT EXISTS (
        SELECT 1 FROM members m
        WHERE m.workspace_id = w.id AND m.role = 'owner'
    );

    IF zero_owner_count > 0 THEN
        RAISE EXCEPTION
            'members owner backfill (0024): % workspace(s) have zero owners; refusing to guess a promotion — assign an owner to each and re-run migrate up',
            zero_owner_count;
    END IF;
END $$;
