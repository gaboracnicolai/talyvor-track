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

// fakeUpdater records every UpdateAICost call so tests can assert the
// syncer pushed the right features through to the issue store.
type fakeUpdater struct {
	mu    sync.Mutex
	calls []struct {
		Feature   string
		CostUSD   float64
		Tokens    int
		Workspace string
	}
	failOn map[string]error
}

func (f *fakeUpdater) UpdateAICost(_ context.Context, feature string, cost float64, tokens int, ws string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failOn[feature]; ok {
		return err
	}
	f.calls = append(f.calls, struct {
		Feature   string
		CostUSD   float64
		Tokens    int
		Workspace string
	}{feature, cost, tokens, ws})
	return nil
}

type fakeWorkspaces struct{ ids []string }

func (f *fakeWorkspaces) ListIDs(context.Context) ([]string, error) { return f.ids, nil }

func TestSyncFeatureSpend_CallsUpdateAICostForEachFeature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[
            {"feature":"ENG-1","cost_usd":1.10,"requests":10,"input_tokens":1000,"output_tokens":500},
            {"feature":"ENG-2","cost_usd":2.20,"requests":20,"input_tokens":2000,"output_tokens":1000}
        ]`)
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "tlv_test")
	updater := &fakeUpdater{}
	syncer := NewSyncer(client, updater, &fakeWorkspaces{ids: []string{"ws-1"}})

	if err := syncer.SyncFeatureSpend(context.Background(), "ws-1"); err != nil {
		t.Fatalf("SyncFeatureSpend: %v", err)
	}
	if len(updater.calls) != 2 {
		t.Fatalf("got %d UpdateAICost calls, want 2", len(updater.calls))
	}
	// tokens = input + output.
	for _, c := range updater.calls {
		switch c.Feature {
		case "ENG-1":
			if c.Tokens != 1500 || c.CostUSD != 1.10 {
				t.Errorf("ENG-1 wrong: %+v", c)
			}
		case "ENG-2":
			if c.Tokens != 3000 || c.CostUSD != 2.20 {
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

func TestSyncFeatureSpend_SkipsEmptyFeatureNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[
            {"feature":"","cost_usd":99.99,"requests":50,"input_tokens":50000,"output_tokens":25000},
            {"feature":"ENG-7","cost_usd":1.00,"requests":5,"input_tokens":500,"output_tokens":250}
        ]`)
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "tlv_test")
	updater := &fakeUpdater{}
	syncer := NewSyncer(client, updater, &fakeWorkspaces{ids: []string{"ws-1"}})

	if err := syncer.SyncFeatureSpend(context.Background(), "ws-1"); err != nil {
		t.Fatalf("SyncFeatureSpend: %v", err)
	}
	if len(updater.calls) != 1 {
		t.Fatalf("got %d calls, want 1 (anonymous feature must be skipped)", len(updater.calls))
	}
	if updater.calls[0].Feature != "ENG-7" {
		t.Errorf("only ENG-7 should have been updated; got %+v", updater.calls[0])
	}
}

func TestSyncFeatureSpend_HandlesLensUnavailableGracefully(t *testing.T) {
	// Closed server — calls will fail.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	client := New(srv.URL, "tlv_test")
	updater := &fakeUpdater{}
	syncer := NewSyncer(client, updater, &fakeWorkspaces{ids: []string{"ws-1"}})

	// Returns nil — never propagates the Lens outage.
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
