package issue_test

import (
	"context"
	"sync"
	"testing"

	"github.com/talyvor/track/internal/issue"
	"github.com/talyvor/track/internal/testutil"
)

func seedFeatureIssue(t *testing.T, d *testutil.DB, wsID, feature string) string {
	t.Helper()
	iss := d.Issue(t, wsID, "")
	if _, err := d.Pool.Exec(context.Background(), `UPDATE issues SET lens_feature=$1 WHERE id=$2`, feature, iss.ID); err != nil {
		t.Fatalf("set lens_feature: %v", err)
	}
	return iss.ID
}

func issueCost(t *testing.T, d *testutil.DB, issueID string) float64 {
	t.Helper()
	var c float64
	if err := d.Pool.QueryRow(context.Background(), `SELECT ai_cost_usd FROM issues WHERE id=$1`, issueID).Scan(&c); err != nil {
		t.Fatalf("issueCost: %v", err)
	}
	return c
}

// ledger returns (sum of cost_usd, row count) of an issue's ai_spend_events.
func ledger(t *testing.T, d *testutil.DB, issueID string) (float64, int) {
	t.Helper()
	var sum float64
	var n int
	if err := d.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(cost_usd),0), count(*) FROM ai_spend_events WHERE issue_id=$1`, issueID).Scan(&sum, &n); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return sum, n
}

// TestRecordSpendEvent_WebhookRedelivery_ExactlyOnce — deliver the same cost event
// twice: the issue is credited exactly once, exactly one ai_spend_events row exists,
// and ledger == aggregate.
func TestRecordSpendEvent_WebhookRedelivery_ExactlyOnce(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	iss := seedFeatureIssue(t, d, ws.ID, "ENG-1")
	st := issue.NewStore(d.Pool)

	key := "lens-spend:deadbeef"
	n1, err := st.RecordSpendEvent(ctx, key, "ENG-1", 2.50, 100, ws.ID, "webhook")
	if err != nil {
		t.Fatal(err)
	}
	n2, err := st.RecordSpendEvent(ctx, key, "ENG-1", 2.50, 100, ws.ID, "webhook") // re-delivery
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 1 {
		t.Errorf("first delivery credited %d issues, want 1", n1)
	}
	if n2 != 0 {
		t.Errorf("re-delivery credited %d issues, want 0 (idempotent)", n2)
	}
	if cost := issueCost(t, d, iss); cost != 2.50 {
		t.Errorf("LEAK: issue cost = %.2f, want 2.50 (re-delivery double-counted)", cost)
	}
	sum, rows := ledger(t, d, iss)
	if rows != 1 {
		t.Errorf("ai_spend_events rows = %d, want exactly 1 (no per-event history would be 0)", rows)
	}
	if sum != issueCost(t, d, iss) {
		t.Errorf("ledger != aggregate: ledger %.2f vs issue %.2f", sum, issueCost(t, d, iss))
	}
}

// NOTE (T7 fu Build 2): the ReconcileFeatureSpend feature-total delta-reconciler was DELETED — the syncer
// now accumulates per-request additively (RecordRequestSpend, keyed exactly-once on request_id, resolving
// the single issue by identifier). Its exactly-once / no-fanout / fail-safe proofs live in
// aicost_per_request_test.go. The RecordSpendEvent (dead webhook path) tests below are retained unchanged.

// TestRecordSpendEvent_ConcurrentIdentical_ExactlyOnce — 16 identical events delivered
// concurrently still credit exactly once (the unique key holds under a race). Run with
// -race.
func TestRecordSpendEvent_ConcurrentIdentical_ExactlyOnce(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	iss := seedFeatureIssue(t, d, ws.ID, "ENG-3")
	st := issue.NewStore(d.Pool)

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = st.RecordSpendEvent(ctx, "lens-spend:concurrent", "ENG-3", 3.00, 0, ws.ID, "webhook")
		}()
	}
	wg.Wait()

	if cost := issueCost(t, d, iss); cost != 3.00 {
		t.Errorf("LEAK: %d concurrent identical events → cost %.2f, want 3.00 (exactly-once under race)", goroutines, cost)
	}
	sum, rows := ledger(t, d, iss)
	if rows != 1 {
		t.Errorf("ledger rows = %d, want 1", rows)
	}
	if sum != issueCost(t, d, iss) {
		t.Errorf("ledger != aggregate: %.2f vs %.2f", sum, issueCost(t, d, iss))
	}
}

// TestSpend_DistinctEventsBothCount — distinct events (different keys) for the same
// issue must BOTH count (idempotency must not over-suppress legitimate events).
func TestSpend_DistinctEventsBothCount(t *testing.T) {
	d := testutil.New(t)
	ctx := context.Background()
	ws := d.Workspace(t)
	iss := seedFeatureIssue(t, d, ws.ID, "ENG-5")
	st := issue.NewStore(d.Pool)

	if _, err := st.RecordSpendEvent(ctx, "lens-spend:a", "ENG-5", 1.00, 0, ws.ID, "webhook"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordSpendEvent(ctx, "lens-spend:b", "ENG-5", 1.50, 0, ws.ID, "webhook"); err != nil {
		t.Fatal(err)
	}
	if cost := issueCost(t, d, iss); cost != 2.50 {
		t.Errorf("two distinct events → cost %.2f, want 2.50 (both must count)", cost)
	}
	sum, rows := ledger(t, d, iss)
	if rows != 2 || sum != 2.50 {
		t.Errorf("ledger rows=%d sum=%.2f, want 2 / 2.50", rows, sum)
	}
}
