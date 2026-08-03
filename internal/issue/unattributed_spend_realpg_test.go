package issue_test

import (
	"context"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

// unattributed_spend_realpg_test.go — the per-issue AI cost must not present itself as the
// whole Lens bill.
//
// WHAT THE SYNCER SKIPS, AND WHY (all three skips are CORRECT and stay):
//
//	(a) feature == ""      a Lens request sent without X-Talyvor-Feature. It addresses no
//	                       issue. Landing it anywhere would be an invented attribution.
//	(b) request_id == ""   cannot be deduplicated. The syncer re-reads the SAME last-24h
//	                       window every 15 minutes (~96×/day), so landing it would multiply
//	                       that cost by the number of pulls.
//	(c) unresolved feature the feature string matches no issue identifier in the workspace
//	                       (a typo, a deleted issue, or a feature name that was never an
//	                       identifier). Landing it would fan cost onto an unrelated issue.
//
// THE DEFECT IS NOT THE SKIP — it is that the skipped money left no trace anywhere a
// customer or an API client can see. skippedCost was summed into a local float and written
// to one slog line, so issues.ai_cost_usd (rendered by the frontend as THE AI cost of an
// issue) is a subset presented as a total, and no per-issue figure can be reconciled
// against the Lens invoice.
//
// THE FIX records (a) and (c) in ai_spend_events with a NULL issue_id — the ledger that was
// ALREADY DESIGNED for them. Migration 0017's own comment says so: "The unique index treats
// a NULL issue_id (orphan spend with no matching issue) as '' so those dedup too." The
// per-request path simply never wrote one. Because both halves are now sums over the SAME
// append-only ledger over the SAME lifetime window, attributed + unattributed = the ledger
// total BY CONSTRUCTION — there is no second number to drift.
//
// (b) still cannot be written: with no request_id there is no dedup key, so it stays a
// counter. That is reported separately rather than folded in, because a number you cannot
// deduplicate is a different kind of number.

// RED: an unresolvable feature recorded NOTHING — the cost vanished from every durable
// surface. GREEN: it lands in the ledger as unattributed, exactly once.
func TestRecordRequestSpend_UnresolvedFeature_LandsAsUnattributed(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	store := issue.NewStore(db.Pool)

	resolved, landed, err := store.RecordRequestSpend(ctx, "req-unresolved-1", "NOPE-999", 4.25, 1200, ws.ID)
	if err != nil {
		t.Fatalf("RecordRequestSpend: %v", err)
	}
	if resolved {
		t.Fatalf("resolved = true for a feature matching no issue identifier")
	}
	if !landed {
		t.Errorf("landed = false — the unattributed cost was not recorded anywhere durable")
	}

	cost, requests, err := store.UnattributedSpend(ctx, ws.ID)
	if err != nil {
		t.Fatalf("UnattributedSpend: %v", err)
	}
	if cost != 4.25 || requests != 1 {
		t.Errorf("unattributed = ($%v, %d requests), want ($4.25, 1) — "+
			"spend that reached no issue must still be visible", cost, requests)
	}
}

// Anonymous spend (no X-Talyvor-Feature) is the LARGEST unattributed bucket in practice —
// any Lens key used without the header. It must be recordable, so an empty feature is
// accepted rather than rejected at the door.
func TestRecordRequestSpend_EmptyFeature_LandsAsUnattributed(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	store := issue.NewStore(db.Pool)

	resolved, landed, err := store.RecordRequestSpend(ctx, "req-anon-1", "", 0.75, 300, ws.ID)
	if err != nil {
		t.Fatalf("RecordRequestSpend with an empty feature: %v", err)
	}
	if resolved {
		t.Errorf("resolved = true for an empty feature")
	}
	if !landed {
		t.Errorf("landed = false — anonymous spend was not recorded")
	}

	cost, requests, err := store.UnattributedSpend(ctx, ws.ID)
	if err != nil {
		t.Fatalf("UnattributedSpend: %v", err)
	}
	if cost != 0.75 || requests != 1 {
		t.Errorf("unattributed = ($%v, %d), want ($0.75, 1)", cost, requests)
	}
}

// EXACTLY ONCE. The syncer re-pulls the same 24h window ~96×/day; an unattributed row must
// not accumulate on every pull. This is the property that makes the number reconcilable
// rather than a running exaggeration.
func TestUnattributedSpend_RepulledWindow_DoesNotDoubleCount(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	store := issue.NewStore(db.Pool)

	for i := 0; i < 5; i++ {
		if _, _, err := store.RecordRequestSpend(ctx, "req-repeat", "GONE-1", 2.00, 500, ws.ID); err != nil {
			t.Fatalf("pull %d: %v", i, err)
		}
	}
	cost, requests, err := store.UnattributedSpend(ctx, ws.ID)
	if err != nil {
		t.Fatalf("UnattributedSpend: %v", err)
	}
	if cost != 2.00 || requests != 1 {
		t.Errorf("after 5 re-pulls unattributed = ($%v, %d), want ($2.00, 1) — "+
			"the 24h window is re-read ~96×/day, so this must dedup by request_id", cost, requests)
	}
}

// A second unattributed row must not collide with the first. The legacy unique index is
// over (event_key, COALESCE(issue_id, empty)) and is NOT partial — two unattributed rows both
// carrying the empty-string event_key default would collide on the SAME key and the second
// INSERT would ERROR. The
// per-request path therefore has to write a per-request event_key.
func TestUnattributedSpend_TwoDistinctRequests_BothLand(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	store := issue.NewStore(db.Pool)

	if _, _, err := store.RecordRequestSpend(ctx, "req-a", "", 1.00, 100, ws.ID); err != nil {
		t.Fatalf("first unattributed row: %v", err)
	}
	if _, _, err := store.RecordRequestSpend(ctx, "req-b", "", 3.00, 200, ws.ID); err != nil {
		t.Fatalf("second unattributed row collided with the first: %v", err)
	}

	cost, requests, err := store.UnattributedSpend(ctx, ws.ID)
	if err != nil {
		t.Fatalf("UnattributedSpend: %v", err)
	}
	if cost != 4.00 || requests != 2 {
		t.Errorf("unattributed = ($%v, %d), want ($4.00, 2)", cost, requests)
	}
}

// THE RECONCILIATION. Attributed + unattributed must equal the whole ledger, because both
// are sums over the same append-only table. This is the property that lets a customer check
// a Track figure against a Lens invoice.
func TestUnattributedPlusAttributed_EqualsTheLedger(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	iss := db.Issue(t, ws.ID, "")
	store := issue.NewStore(db.Pool)

	// One request that resolves onto the issue…
	resolved, _, err := store.RecordRequestSpend(ctx, "req-hit", iss.Identifier, 10.00, 1000, ws.ID)
	if err != nil {
		t.Fatalf("attributed: %v", err)
	}
	if !resolved {
		t.Fatalf("seeded issue %q did not resolve — the fixture is wrong, not the code", iss.Identifier)
	}
	// …and two that do not.
	if _, _, err := store.RecordRequestSpend(ctx, "req-miss-1", "", 2.50, 100, ws.ID); err != nil {
		t.Fatalf("anonymous: %v", err)
	}
	if _, _, err := store.RecordRequestSpend(ctx, "req-miss-2", "DELETED-7", 1.50, 100, ws.ID); err != nil {
		t.Fatalf("unresolved: %v", err)
	}

	var attributed, ledger float64
	if err := db.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(ai_cost_usd),0) FROM issues WHERE workspace_id=$1`, ws.ID).Scan(&attributed); err != nil {
		t.Fatalf("sum issues: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(cost_usd),0) FROM ai_spend_events WHERE workspace_id=$1`, ws.ID).Scan(&ledger); err != nil {
		t.Fatalf("sum ledger: %v", err)
	}
	unattributed, _, err := store.UnattributedSpend(ctx, ws.ID)
	if err != nil {
		t.Fatalf("UnattributedSpend: %v", err)
	}

	if attributed != 10.00 {
		t.Errorf("attributed = $%v, want $10.00", attributed)
	}
	if unattributed != 4.00 {
		t.Errorf("unattributed = $%v, want $4.00", unattributed)
	}
	if attributed+unattributed != ledger {
		t.Errorf("attributed $%v + unattributed $%v = $%v, but the ledger holds $%v — "+
			"the two halves must partition the ledger or neither number can be reconciled",
			attributed, unattributed, attributed+unattributed, ledger)
	}
}

// Unattributed spend is per-workspace. One tenant's untagged Lens usage must never appear
// in another's reconciliation.
func TestUnattributedSpend_IsWorkspaceScoped(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	wsA, wsB := db.Workspace(t), db.Workspace(t)
	store := issue.NewStore(db.Pool)

	if _, _, err := store.RecordRequestSpend(ctx, "req-a-only", "", 9.99, 100, wsA.ID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cost, requests, err := store.UnattributedSpend(ctx, wsB.ID)
	if err != nil {
		t.Fatalf("UnattributedSpend(wsB): %v", err)
	}
	if cost != 0 || requests != 0 {
		t.Errorf("wsB sees ($%v, %d) of wsA's unattributed spend — cross-tenant leak", cost, requests)
	}
}

// A request_id-less row has no dedup key, so it must be REFUSED rather than written — the
// re-pulled window would multiply it. This pins the one skip that stays a skip.
func TestRecordRequestSpend_NoRequestID_IsRefused(t *testing.T) {
	db := testutil.New(t)
	ctx := context.Background()
	ws := db.Workspace(t)
	store := issue.NewStore(db.Pool)

	if _, _, err := store.RecordRequestSpend(ctx, "", "ENG-1", 5.00, 100, ws.ID); err == nil {
		t.Errorf("a row with no request_id was accepted — it cannot be deduplicated, so the " +
			"~96 pulls/day of the same window would each re-credit it")
	}
	cost, _, err := store.UnattributedSpend(ctx, ws.ID)
	if err != nil {
		t.Fatalf("UnattributedSpend: %v", err)
	}
	if cost != 0 {
		t.Errorf("unattributed = $%v after a refused write, want $0", cost)
	}
}
