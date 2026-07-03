package lensintegration

import (
	"context"
	"log/slog"
	"time"
)

const defaultSyncInterval = 15 * time.Minute

// costUpdater is the slice of internal/issue.Store the syncer needs.
// Defined as an interface so tests can drop in a counter mock without
// spinning up pgxmock or the full Track DB schema.
type costUpdater interface {
	// RecordRequestSpend lands one per-request cost on the single identifier-matched issue, exactly-once by
	// request_id. resolved=false ⇒ the feature addresses no issue (the caller skips + logs the orphan).
	RecordRequestSpend(ctx context.Context, requestID, feature string, costUSD float64, tokens int, workspaceID string) (resolved, landed bool, err error)
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

// SyncFeatureSpend pulls last-24h PER-REQUEST spend from Lens for one workspace and lands each request's cost
// on the single identifier-matched issue, exactly-once by request_id (T7 follow-up, Build 2). The cost never
// fans out (resolution is by identifier, not lens_feature), and a re-pulled window — the syncer re-reads the
// same 24h ~96×/day — re-credits nothing (ON CONFLICT). Errors are logged at WARN; a missing Lens or a
// partial outage never breaks Track.
//
// FAIL-SAFE: a row whose feature doesn't resolve to exactly one issue (0 identifier matches), or an anonymous
// / request_id-less row, is SKIPPED and counted — never written as an orphan, never fanned out. The skipped
// count + skipped cost are logged so orphan spend is observable, not silently dropped.
func (s *Syncer) SyncFeatureSpend(ctx context.Context, workspaceID string) error {
	if s.client == nil || !s.client.IsConfigured() {
		return ErrNotConfigured
	}
	rows, err := s.client.GetSpendByRequest(ctx, workspaceID, 1)
	if err != nil {
		slog.Warn("lensintegration: sync failed",
			slog.String("workspace_id", workspaceID),
			slog.String("err", err.Error()),
		)
		return nil
	}
	var landed, skipped int
	var skippedCost float64
	for _, rs := range rows {
		if rs.Feature == "" || rs.RequestID == "" {
			// Anonymous spend (no X-Talyvor-Feature) or a row without a request_id: can't address one issue
			// or can't dedup exactly-once. Skip rather than risk an orphan or a double-count.
			skipped++
			skippedCost += rs.CostUSD
			continue
		}
		resolved, didLand, err := s.updater.RecordRequestSpend(ctx, rs.RequestID, rs.Feature, rs.CostUSD, rs.InputTokens+rs.OutputTokens, workspaceID)
		if err != nil {
			slog.Warn("lensintegration: RecordRequestSpend failed",
				slog.String("workspace_id", workspaceID),
				slog.String("feature", rs.Feature),
				slog.String("request_id", rs.RequestID),
				slog.String("err", err.Error()),
			)
			continue
		}
		if !resolved {
			// FAIL-SAFE: the feature addresses no issue (identifier match = 0). Never write an orphan, never
			// fall back to the lens_feature fanout. Log so the orphan cost stays observable.
			skipped++
			skippedCost += rs.CostUSD
			slog.Warn("lensintegration: request spend skipped — feature resolves to no issue",
				slog.String("workspace_id", workspaceID),
				slog.String("feature", rs.Feature),
				slog.String("request_id", rs.RequestID),
				slog.Float64("cost_usd", rs.CostUSD),
			)
			continue
		}
		if didLand {
			landed++
		}
		// resolved && !didLand ⇒ this request_id already landed on an earlier pull; not re-credited.
	}
	slog.Info("lensintegration: per-request spend sync complete",
		slog.String("workspace_id", workspaceID),
		slog.Int("landed", landed),
		slog.Int("skipped", skipped),
		slog.Float64("skipped_cost_usd", skippedCost),
		slog.Int("total_rows", len(rows)),
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
