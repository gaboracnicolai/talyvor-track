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
	// RecordRequestSpend lands one per-request cost EXACTLY ONCE by request_id: on the single
	// identifier-matched issue when the feature resolves, and as an UNATTRIBUTED ledger row
	// (NULL issue_id, no issue credited) when it does not. resolved=false therefore still means
	// "no issue was credited" — but the money is now recorded either way.
	RecordRequestSpend(ctx context.Context, requestID, feature string, costUSD float64, tokens int, workspaceID string) (resolved, landed bool, err error)
	// RecordRequestSpendAttributed is the same write with the issue the caller was working on
	// preferred over the feature — see issue.Store for why the fallback is load-bearing.
	RecordRequestSpendAttributed(ctx context.Context, requestID, feature, issueIdentifier string, costUSD float64, tokens int, workspaceID string) (resolved, landed bool, err error)
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
// NO COST IS EVER CREDITED TO AN ISSUE IT DOES NOT BELONG TO — but nothing dedupable is dropped
// either. A row whose feature resolves to no issue (untagged spend, or a feature matching no
// identifier) is recorded as UNATTRIBUTED: a ledger row with a NULL issue_id, no issue credited.
// That distinction is the whole point — the attribution is refused, the accounting is not.
//
// This used to `continue` before the store on an empty feature and discard the unresolved case,
// summing both into a local float that reached exactly one slog line. issues.ai_cost_usd — which
// the frontend renders as THE AI cost of an issue — was therefore a subset presented as a total,
// and no figure could be reconciled against the Lens invoice. Read the recorded total back with
// issue.Store.UnattributedSpend; both AI-cost endpoints surface it.
//
// THE ONE ROW STILL REFUSED is a row with no request_id: it has no dedup key, and this same 24h
// window is re-read ~96×/day, so writing it would multiply that cost by the number of pulls. It
// is counted and logged SEPARATELY rather than folded into the unattributed total — a number that
// cannot be deduplicated must not be added to one that can.
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
	var attributed, unattributed, undedupable int
	var unattributedCost, undedupableCost float64
	for _, rs := range rows {
		if rs.RequestID == "" {
			// No dedup key. Cannot be written at any cost — the re-pulled window would
			// re-credit it on every tick. Counted so it is still visible.
			undedupable++
			undedupableCost += rs.CostUSD
			continue
		}
		// An empty feature is passed through deliberately: it resolves to nothing and lands
		// as unattributed. It is the LARGEST such bucket in practice — any Lens key used
		// without X-Talyvor-Feature.
		// ⚠ THE ISSUE IS PREFERRED, THE FEATURE IS THE FALLBACK. The Code extension sends the
		// feature as an IDE affordance ("code-chat"), so matching on it credited nothing for every
		// request from the editor we ship. It also sends the issue, which Lens now returns. An
		// empty issue resolves exactly as before, which is what keeps manual taggers working.
		resolved, didLand, err := s.updater.RecordRequestSpendAttributed(ctx, rs.RequestID, rs.Feature, rs.IssueID, rs.CostUSD, rs.InputTokens+rs.OutputTokens, workspaceID)
		if err != nil {
			slog.Warn("lensintegration: RecordRequestSpend failed",
				slog.String("workspace_id", workspaceID),
				slog.String("feature", rs.Feature),
				slog.String("issue", rs.IssueID),
				slog.String("request_id", rs.RequestID),
				slog.String("err", err.Error()),
			)
			continue
		}
		if !didLand {
			continue // already recorded on an earlier pull; nothing re-credited, nothing re-counted
		}
		if resolved {
			attributed++
			continue
		}
		unattributed++
		unattributedCost += rs.CostUSD
	}
	slog.Info("lensintegration: per-request spend sync complete",
		slog.String("workspace_id", workspaceID),
		slog.Int("attributed", attributed),
		slog.Int("unattributed", unattributed),
		slog.Float64("unattributed_cost_usd", unattributedCost),
		slog.Int("undedupable", undedupable),
		slog.Float64("undedupable_cost_usd", undedupableCost),
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
