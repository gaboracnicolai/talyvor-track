package lensintegration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeUpdater records every RecordRequestSpend call so tests can assert the syncer pushed the right rows
// through. resolveMiss lists features it should report as unresolved (resolved=false) to exercise the
// fail-safe skip.
type fakeUpdater struct {
	mu    sync.Mutex
	calls []struct {
		RequestID string
		Feature   string
		CostUSD   float64
		Tokens    int
		Workspace string
	}
	resolveMiss map[string]bool
	failOn      map[string]error
}

func (f *fakeUpdater) RecordRequestSpend(_ context.Context, requestID, feature string, cost float64, tokens int, ws string) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failOn[feature]; ok {
		return false, false, err
	}
	f.calls = append(f.calls, struct {
		RequestID string
		Feature   string
		CostUSD   float64
		Tokens    int
		Workspace string
	}{requestID, feature, cost, tokens, ws})
	if f.resolveMiss[feature] {
		return false, false, nil // feature resolves to no issue → syncer skips
	}
	return true, true, nil // resolved + landed
}

type fakeWorkspaces struct{ ids []string }

func (f *fakeWorkspaces) ListIDs(context.Context) ([]string, error) { return f.ids, nil }

// byRequestBody wraps rows in the {rows, next_cursor} envelope the by-request endpoint returns.
func byRequestBody(rows string) string { return `{"rows":` + rows + `,"next_cursor":""}` }

func TestSyncFeatureSpend_LandsEachRequestRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, byRequestBody(`[
            {"request_id":"r1","feature":"ENG-1","cost_usd":1.10,"input_tokens":1000,"output_tokens":500,"ts":"2026-07-01T00:00:00Z"},
            {"request_id":"r2","feature":"ENG-2","cost_usd":2.20,"input_tokens":2000,"output_tokens":1000,"ts":"2026-07-01T00:00:01Z"}
        ]`))
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "tlv_test")
	updater := &fakeUpdater{}
	syncer := NewSyncer(client, updater, &fakeWorkspaces{ids: []string{"ws-1"}})

	if err := syncer.SyncFeatureSpend(context.Background(), "ws-1"); err != nil {
		t.Fatalf("SyncFeatureSpend: %v", err)
	}
	if len(updater.calls) != 2 {
		t.Fatalf("got %d RecordRequestSpend calls, want 2", len(updater.calls))
	}
	for _, c := range updater.calls {
		switch c.Feature {
		case "ENG-1":
			if c.RequestID != "r1" || c.Tokens != 1500 || c.CostUSD != 1.10 {
				t.Errorf("ENG-1 wrong: %+v", c)
			}
		case "ENG-2":
			if c.RequestID != "r2" || c.Tokens != 3000 || c.CostUSD != 2.20 {
				t.Errorf("ENG-2 wrong: %+v", c)
			}
		default:
			t.Errorf("unexpected feature: %s", c.Feature)
		}
		if c.Workspace != "ws-1" {
			t.Errorf("workspace = %q, want ws-1", c.Workspace)
		}
	}
}

// This test used to assert that an empty FEATURE and an empty REQUEST_ID were both skipped
// before the writer. Only the second half was ever right, and the two were bundled under one
// count so the difference could not be seen:
//
//   - empty request_id — CORRECTLY skipped. There is no dedup key, and the same 24h window is
//     re-read ~96×/day, so writing it would re-credit that cost on every tick. UNCHANGED.
//   - empty feature — the DEFECT. The row below carries $99.99 and 75k tokens precisely so a
//     silent drop is expensive, and dropping it is exactly what happened: untagged spend
//     reached no durable surface at all, leaving issues.ai_cost_usd a subset presented as a
//     total. It now reaches the writer and lands UNATTRIBUTED (NULL issue_id, no issue
//     credited) — the attribution is still refused, the accounting no longer is.
//
// Both rules are asserted separately here so neither can change again behind a count.
func TestSyncFeatureSpend_UntaggedRowIsRecorded_RequestIDLessRowIsNot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, byRequestBody(`[
            {"request_id":"r0","feature":"","cost_usd":99.99,"input_tokens":50000,"output_tokens":25000,"ts":"t"},
            {"request_id":"","feature":"ENG-9","cost_usd":50.00,"input_tokens":1,"output_tokens":1,"ts":"t"},
            {"request_id":"r7","feature":"ENG-7","cost_usd":1.00,"input_tokens":500,"output_tokens":250,"ts":"t"}
        ]`))
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "tlv_test")
	updater := &fakeUpdater{}
	syncer := NewSyncer(client, updater, &fakeWorkspaces{ids: []string{"ws-1"}})

	if err := syncer.SyncFeatureSpend(context.Background(), "ws-1"); err != nil {
		t.Fatalf("SyncFeatureSpend: %v", err)
	}

	seen := map[string]bool{}
	for _, c := range updater.calls {
		seen[c.RequestID] = true
		if c.RequestID == "" {
			t.Errorf("a row with no request_id reached the writer — it cannot be deduplicated, "+
				"so the ~96 re-pulls/day of this window would each re-credit it: %+v", c)
		}
	}
	if !seen["r0"] {
		t.Errorf("the untagged $99.99 row never reached the writer — untagged spend must be "+
			"recorded as unattributed, not dropped; calls: %+v", updater.calls)
	}
	if !seen["r7"] {
		t.Errorf("the attributable ENG-7 row never reached the writer; calls: %+v", updater.calls)
	}
	if len(updater.calls) != 2 {
		t.Errorf("got %d calls, want 2 (r0 unattributed + r7 attributed; the request_id-less "+
			"row is the only one refused): %+v", len(updater.calls), updater.calls)
	}
}

func TestSyncFeatureSpend_HandlesLensUnavailableGracefully(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // closed server — calls will fail

	client := New(srv.URL, "tlv_test")
	updater := &fakeUpdater{}
	syncer := NewSyncer(client, updater, &fakeWorkspaces{ids: []string{"ws-1"}})

	if err := syncer.SyncFeatureSpend(context.Background(), "ws-1"); err != nil {
		t.Errorf("SyncFeatureSpend should swallow Lens errors; got %v", err)
	}
	if len(updater.calls) != 0 {
		t.Errorf("no calls should fire when Lens is unreachable; got %d", len(updater.calls))
	}
}

func TestSyncFeatureSpend_ReturnsErrNotConfiguredWhenLensEmpty(t *testing.T) {
	client := New("", "")
	syncer := NewSyncer(client, &fakeUpdater{}, &fakeWorkspaces{ids: []string{"ws-1"}})
	err := syncer.SyncFeatureSpend(context.Background(), "ws-1")
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured; got %v", err)
	}
}
