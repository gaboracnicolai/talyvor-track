package issue_test

import (
	"context"
	"sync"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

func setLensFeature(t *testing.T, d *testutil.DB, issueID, feature string) {
	t.Helper()
	if _, err := d.Pool.Exec(context.Background(), `UPDATE issues SET lens_feature=$1 WHERE id=$2`, feature, issueID); err != nil {
		t.Fatalf("set lens_feature: %v", err)
	}
}

// (proof a — RED baseline) Without the request_id guard, the syncer's ~96×/day re-pull of the same window
// over-counts. This documents WHY the unique index + ON CONFLICT is load-bearing: drop the index and
// accumulate naively (plain INSERT + always-credit) and the same request lands 96×.
func TestPerRequest_WithoutGuard_OverCounts_RED(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	iss := d.Issue(t, ws.ID, "")

	if _, err := d.Pool.Exec(ctx, `DROP INDEX IF EXISTS uq_ai_spend_events_request`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 96; i++ { // 96 = last-24h re-pulled every 15min
		// Naive: no request_id dedup (index dropped). Distinct event_key per row so the legacy
		// (event_key, issue_id) index permits them — isolating the MISSING request_id guard as the bug.
		if _, err := d.Pool.Exec(ctx,
			`INSERT INTO ai_spend_events (request_id, event_key, workspace_id, issue_id, cost_usd, tokens, source)
			 VALUES ('req-X', gen_random_uuid()::text, $1, $2, 1.00, 0, 'naive')`, ws.ID, iss.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool.Exec(ctx, `UPDATE issues SET ai_cost_usd = ai_cost_usd + 1.00 WHERE id=$1`, iss.ID); err != nil {
			t.Fatal(err)
		}
	}
	if cost := issueCost(t, d, iss.ID); cost != 96.00 {
		t.Fatalf("RED baseline: naive accumulation must OVER-COUNT to 96.00, got %.2f", cost)
	} // ← this is the bug the real writer (below) prevents.
}

// (proof a — GREEN) 96 overlapping re-runs over the SAME request through the real writer land the cost
// EXACTLY ONCE. Explicitly covers the retried-already-landed edge: pass 0 lands (resolved+landed); passes
// 1..95 re-pull the same request_id ⇒ ON CONFLICT ⇒ resolved but NOT landed, and the issue credit does not
// move. (The unique index from migration 0019 is the ON CONFLICT arbiter.)
func TestPerRequest_ExactlyOnce_OverlappingReruns(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	iss := d.Issue(t, ws.ID, "")
	st := issue.NewStore(d.Pool)

	for i := 0; i < 96; i++ {
		resolved, landed, err := st.RecordRequestSpend(ctx, "req-X", iss.Identifier, 1.00, 10, ws.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !resolved {
			t.Fatalf("pass %d: feature=%q must resolve to the issue", i, iss.Identifier)
		}
		if i == 0 && !landed {
			t.Fatal("pass 0 must LAND (fresh insert)")
		}
		if i > 0 && landed {
			t.Fatalf("pass %d: a re-pulled request_id must NOT re-land (exactly-once)", i)
		}
	}
	if cost := issueCost(t, d, iss.ID); cost != 1.00 {
		t.Fatalf("EXACTLY-ONCE: 96 re-runs of $1 → cost %.2f, want 1.00 (never 96×)", cost)
	}
	sum, rows := ledger(t, d, iss.ID)
	if rows != 1 || sum != 1.00 {
		t.Fatalf("ledger must have exactly 1 row summing 1.00, got rows=%d sum=%.2f", rows, sum)
	}
}

// (proof b) NO-FANOUT: 3 issues share one lens_feature; a request whose feature = ONE issue's identifier
// lands ONLY on that issue; the other two get ZERO. (The old reconciler fanned to all 3.)
func TestPerRequest_NoFanout_LandsOnIdentifierOnly(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	a := d.Issue(t, ws.ID, "")
	b := d.Issue(t, ws.ID, "")
	c := d.Issue(t, ws.ID, "")
	// All three SHARE the lens_feature the old fanout matched on.
	setLensFeature(t, d, a.ID, "shared-feat")
	setLensFeature(t, d, b.ID, "shared-feat")
	setLensFeature(t, d, c.ID, "shared-feat")

	// The request addresses issue A by IDENTIFIER (the 1:1 path).
	resolved, landed, err := st(d).RecordRequestSpend(ctx, "req-1", a.Identifier, 5.00, 0, ws.ID)
	if err != nil || !resolved || !landed {
		t.Fatalf("must land on A: resolved=%v landed=%v err=%v", resolved, landed, err)
	}
	if cost := issueCost(t, d, a.ID); cost != 5.00 {
		t.Fatalf("issue A must be credited 5.00, got %.2f", cost)
	}
	if cost := issueCost(t, d, b.ID); cost != 0 {
		t.Fatalf("FANOUT REGRESSION: issue B (shares lens_feature) must be 0, got %.2f", cost)
	}
	if cost := issueCost(t, d, c.ID); cost != 0 {
		t.Fatalf("FANOUT REGRESSION: issue C (shares lens_feature) must be 0, got %.2f", cost)
	}
}

// (proof c) NO-DOUBLE-COUNT across the model switch: total landed == sum of distinct per-request costs;
// no feature-total residue.
func TestPerRequest_NoDoubleCount_TotalEqualsDistinctSum(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	iss := d.Issue(t, ws.ID, "")
	store := st(d)

	costs := map[string]float64{"r1": 0.11, "r2": 0.22, "r3": 0.33, "r4": 0.44}
	// Two full syncer passes over the same request set (overlap).
	for pass := 0; pass < 2; pass++ {
		for rid, cost := range costs {
			if _, _, err := store.RecordRequestSpend(ctx, rid, iss.Identifier, cost, 0, ws.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := 0.11 + 0.22 + 0.33 + 0.44
	if cost := issueCost(t, d, iss.ID); cost < want-1e-9 || cost > want+1e-9 {
		t.Fatalf("total landed = %.6f, want %.6f (distinct sum, no double-count, no residue)", cost, want)
	}
	_, rows := ledger(t, d, iss.ID)
	if rows != len(costs) {
		t.Fatalf("ledger rows = %d, want %d (one per distinct request)", rows, len(costs))
	}
}

// (proof d) FAIL-SAFE: (i) a feature matching NO identifier → skipped, zero rows, no orphan. (ii) a feature
// that is a shared lens_feature on >1 issues (but no identifier) → skipped, never fanned out.
func TestPerRequest_FailSafe_SkipsUnresolvable(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	store := st(d)

	// (i) no issue has identifier = "GHOST-1".
	resolved, landed, err := store.RecordRequestSpend(ctx, "req-ghost", "GHOST-1", 9.99, 0, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved || landed {
		t.Fatalf("unresolvable feature must SKIP: resolved=%v landed=%v", resolved, landed)
	}
	var ghostRows int
	_ = d.Pool.QueryRow(ctx, `SELECT count(*) FROM ai_spend_events WHERE request_id='req-ghost'`).Scan(&ghostRows)
	if ghostRows != 0 {
		t.Fatalf("no orphan row must be written for an unresolvable feature, got %d", ghostRows)
	}

	// (ii) two issues share lens_feature "dup-feat"; no issue has identifier="dup-feat".
	x := d.Issue(t, ws.ID, "")
	y := d.Issue(t, ws.ID, "")
	setLensFeature(t, d, x.ID, "dup-feat")
	setLensFeature(t, d, y.ID, "dup-feat")
	resolved2, landed2, err := store.RecordRequestSpend(ctx, "req-dup", "dup-feat", 7.00, 0, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved2 || landed2 {
		t.Fatalf("shared-lens_feature (no identifier) must SKIP, never fan out: resolved=%v landed=%v", resolved2, landed2)
	}
	if cx, cy := issueCost(t, d, x.ID), issueCost(t, d, y.ID); cx != 0 || cy != 0 {
		t.Fatalf("FANOUT: shared-lens_feature issues must stay 0, got X=%.2f Y=%.2f", cx, cy)
	}
}

// (proof e) CONCURRENCY: many goroutines racing the SAME request_id → exactly one insert, issue credited
// once. Run with -race.
func TestPerRequest_ConcurrentSameRequest_ExactlyOnce(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	iss := d.Issue(t, ws.ID, "")
	store := st(d)

	const N = 16
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = store.RecordRequestSpend(ctx, "req-race", iss.Identifier, 3.00, 0, ws.ID)
		}()
	}
	wg.Wait()

	if cost := issueCost(t, d, iss.ID); cost != 3.00 {
		t.Fatalf("CONCURRENCY: %d racers on one request_id → cost %.2f, want 3.00 (exactly one credit)", N, cost)
	}
	sum, rows := ledger(t, d, iss.ID)
	if rows != 1 || sum != 3.00 {
		t.Fatalf("exactly one ledger row summing 3.00, got rows=%d sum=%.2f", rows, sum)
	}
}

// st builds an issue.Store on the harness pool (a tiny alias to keep the proofs terse).
func st(d *testutil.DB) *issue.Store { return issue.NewStore(d.Pool) }
