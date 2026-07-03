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

func TestSyncFeatureSpend_SkipsEmptyFeatureOrRequestID(t *testing.T) {
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
	if len(updater.calls) != 1 {
		t.Fatalf("got %d calls, want 1 (empty feature + empty request_id must be skipped before the writer)", len(updater.calls))
	}
	if updater.calls[0].Feature != "ENG-7" || updater.calls[0].RequestID != "r7" {
		t.Errorf("only the ENG-7/r7 row should reach the writer; got %+v", updater.calls[0])
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
