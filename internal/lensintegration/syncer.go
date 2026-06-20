package lensintegration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"
)

const defaultSyncInterval = 15 * time.Minute

// costUpdater is the slice of internal/issue.Store the syncer needs.
// Defined as an interface so tests can drop in a counter mock without
// spinning up pgxmock or the full Track DB schema.
type costUpdater interface {
	ReconcileFeatureSpend(ctx context.Context, eventKey, lensFeature string, lensTotalUSD float64, lensTokens int, workspaceID string) (float64, error)
}

// workspaceLister returns the workspace IDs the syncer should poll on
// every tick. Phase 4 wires this to workspace.Store.ListIDs.
type workspaceLister interface {
	ListIDs(ctx context.Context) ([]string, error)
}

type Syncer struct {
	client     *Client
	updater    costUpdater
	workspaces workspaceLister
}

func NewSyncer(client *Client, updater costUpdater, workspaces workspaceLister) *Syncer {
	return &Syncer{client: client, updater: updater, workspaces: workspaces}
}

// SyncFeatureSpend pulls last-24h spend-by-feature from Lens for one
// workspace and accumulates the cost on every issue whose
// lens_feature matches. Errors are logged at WARN — a missing Lens
// or a partial outage never breaks Track.
func (s *Syncer) SyncFeatureSpend(ctx context.Context, workspaceID string) error {
	if s.client == nil || !s.client.IsConfigured() {
		return ErrNotConfigured
	}
	features, err := s.client.GetSpendByFeature(ctx, workspaceID, 1)
	if err != nil {
		slog.Warn("lensintegration: sync failed",
			slog.String("workspace_id", workspaceID),
			slog.String("err", err.Error()),
		)
		return nil
	}
	synced := 0
	for _, fs := range features {
		if fs.Feature == "" {
			// Anonymous spend (no X-Talyvor-Feature header set on the
			// originating request) doesn't map to a Track issue.
			continue
		}
		// Reconcile against the ledger: the key is the observed (workspace, feature,
		// total), so a repeated poll of an unchanged total is a no-op, and the
		// reconciler adds only the gap the webhook hasn't already recorded — it never
		// double-counts spend the webhook already wrote.
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%v", workspaceID, fs.Feature, fs.CostUSD)))
		eventKey := "lens-sync:" + hex.EncodeToString(sum[:])
		if _, err := s.updater.ReconcileFeatureSpend(ctx, eventKey, fs.Feature, fs.CostUSD, fs.InputTokens+fs.OutputTokens, workspaceID); err != nil {
			slog.Warn("lensintegration: ReconcileFeatureSpend failed",
				slog.String("workspace_id", workspaceID),
				slog.String("feature", fs.Feature),
				slog.String("err", err.Error()),
			)
			continue
		}
		synced++
	}
	slog.Info("lensintegration: feature spend sync complete",
		slog.String("workspace_id", workspaceID),
		slog.Int("synced", synced),
		slog.Int("total_features", len(features)),
	)
	return nil
}

// StartSync runs SyncFeatureSpend across every workspace on a ticker.
// Default interval 15 minutes. Exits on ctx.Done(). Workspace
// enumeration failures are logged and the tick continues — the next
// tick retries automatically.
func (s *Syncer) StartSync(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultSyncInterval
	}
	// Run once at start so the dashboard isn't empty for 15 minutes
	// after boot.
	s.runOnce(ctx)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runOnce(ctx)
		}
	}
}

func (s *Syncer) runOnce(ctx context.Context) {
	if !s.client.IsConfigured() {
		return
	}
	ids, err := s.workspaces.ListIDs(ctx)
	if err != nil {
		slog.Warn("lensintegration: workspace listing failed",
			slog.String("err", err.Error()),
		)
		return
	}
	for _, ws := range ids {
		if ctx.Err() != nil {
			return
		}
		_ = s.SyncFeatureSpend(ctx, ws)
	}
}
