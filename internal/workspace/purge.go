package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// purgeOrder removes everything a workspace owns, children before the rows they reference. Most child
// tables REFERENCE their parent with no ON DELETE action, so a single `DELETE FROM workspaces` is refused;
// deleting in this order satisfies every constraint without changing one. The tables whose foreign keys
// already CASCADE or SET NULL (comments, custom fields, templates, guests, scores, feature boards, import
// payloads …) go with the rows below.
//
// A table added later that REFERENCES workspaces(id) without a cascade makes the final DELETE fail, the
// transaction roll back, and the sweep log the constraint by name — the workspace is kept, never half-removed.
var purgeOrder = []string{
	`DELETE FROM automation_logs WHERE rule_id IN (SELECT id FROM automation_rules WHERE workspace_id = $1)
	    OR issue_id IN (SELECT id FROM issues WHERE workspace_id = $1)`,
	`DELETE FROM automation_rules WHERE workspace_id = $1`,
	`DELETE FROM notifications WHERE workspace_id = $1`,
	`DELETE FROM ai_spend_events WHERE workspace_id = $1`,
	`DELETE FROM time_entries WHERE workspace_id = $1`,
	`DELETE FROM issue_relations WHERE workspace_id = $1`,
	`DELETE FROM issues WHERE workspace_id = $1`,
	`DELETE FROM milestones WHERE workspace_id = $1`,
	`DELETE FROM cycles WHERE workspace_id = $1`,
	`DELETE FROM projects WHERE workspace_id = $1`,
	`DELETE FROM labels WHERE workspace_id = $1`,
	`DELETE FROM workflow_statuses WHERE team_id IN (SELECT id FROM teams WHERE workspace_id = $1)`,
	`DELETE FROM teams WHERE workspace_id = $1`,
	`DELETE FROM members WHERE workspace_id = $1`,
	`DELETE FROM import_jobs WHERE workspace_id = $1`,
	`DELETE FROM workspace_integrations WHERE workspace_id = $1`,
	`DELETE FROM workspaces WHERE id = $1`,
}

// PurgeExpired permanently removes every workspace deleted more than RestoreWindow before now, one
// transaction per workspace. It returns how many were removed; a workspace that fails is logged, left
// intact, and retried on the next sweep.
func (s *Store) PurgeExpired(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Add(-RestoreWindow)
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM workspaces WHERE deleted_at IS NOT NULL AND deleted_at <= $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("workspace: find expired: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	purged := 0
	for _, id := range ids {
		if err := s.purge(ctx, id, cutoff); err != nil {
			slog.Error("workspace: purge of a deleted workspace failed; it is kept and retried on the next sweep",
				slog.String("workspace_id", id), slog.String("err", err.Error()))
			continue
		}
		purged++
	}
	return purged, nil
}

func (s *Store) purge(ctx context.Context, id string, cutoff time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Lock the row and re-check it is still past its window, so a restore that landed after the scan
	// above keeps its workspace.
	var still bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM workspaces WHERE id = $1 AND deleted_at IS NOT NULL AND deleted_at <= $2 FOR UPDATE)`,
		id, cutoff).Scan(&still); err != nil {
		return err
	}
	if !still {
		return nil
	}
	for _, stmt := range purgeOrder {
		if _, err := tx.Exec(ctx, stmt, id); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return tx.Commit(ctx)
}

// StartPurge runs PurgeExpired every interval until ctx ends.
func (s *Store) StartPurge(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.PurgeExpired(ctx, time.Now()); err != nil {
				slog.Warn("workspace: purge sweep failed", slog.String("err", err.Error()))
			} else if n > 0 {
				slog.Info("workspace: purged deleted workspaces", slog.Int("count", n))
			}
		}
	}
}
