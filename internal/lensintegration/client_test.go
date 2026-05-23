package lensintegration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockLens spins up an httptest server that responds with the supplied
// body for any path. Tests configure it once and reuse for the whole
// scenario.
func mockLens(t *testing.T, paths map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := paths[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetSpendSummary_ReturnsCorrectTotals(t *testing.T) {
	srv := mockLens(t, map[string]string{
		"/v1/api/spend/summary": `{
            "total_cost_usd": 42.50,
            "total_input_tokens": 1000000,
            "total_output_tokens": 500000,
            "total_requests": 3200,
            "cache_hit_rate": 0.62,
            "avg_cost_per_request": 0.013,
            "period_days": 30
        }`,
	})
	c := New(srv.URL, "tlv_test")

	got, err := c.GetSpendSummary(context.Background(), "ws-1", 30)
	if err != nil {
		t.Fatalf("GetSpendSummary: %v", err)
	}
	if got.TotalCostUSD != 42.50 {
		t.Errorf("TotalCostUSD = %v, want 42.50", got.TotalCostUSD)
	}
	if got.PeriodDays != 30 {
		t.Errorf("PeriodDays = %d, want 30", got.PeriodDays)
	}
	if got.CacheHitRate < 0.61 || got.CacheHitRate > 0.63 {
		t.Errorf("CacheHitRate = %v, want ~0.62", got.CacheHitRate)
	}
}

func TestGetSpendByFeature_ReturnsBreakdown(t *testing.T) {
	srv := mockLens(t, map[string]string{
		"/v1/api/spend/by-feature": `[
            {"feature":"ENG-42","cost_usd":12.30,"requests":80,"input_tokens":50000,"output_tokens":15000},
            {"feature":"ENG-43","cost_usd":3.10,"requests":20,"input_tokens":10000,"output_tokens":3000}
        ]`,
	})
	c := New(srv.URL, "tlv_test")

	got, err := c.GetSpendByFeature(context.Background(), "ws-1", 1)
	if err != nil {
		t.Fatalf("GetSpendByFeature: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Feature != "ENG-42" || got[0].CostUSD != 12.30 {
		t.Errorf("entry[0] wrong: %+v", got[0])
	}
}

func TestGetAnomalies_ReturnsList(t *testing.T) {
	srv := mockLens(t, map[string]string{
		"/v1/api/anomalies": `[
            {"type":"spike","feature":"ENG-99","deviation_sigma":4.2,"message":"Cost spike: $5.20 vs $0.40 baseline"}
        ]`,
	})
	c := New(srv.URL, "tlv_test")

	got, err := c.GetAnomalies(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("GetAnomalies: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0]["type"] != "spike" {
		t.Errorf("type = %v, want spike", got[0]["type"])
	}
}

func TestHealthy_ReturnsTrueWhenLensUp(t *testing.T) {
	srv := mockLens(t, map[string]string{
		"/v1/api/health": `{"ok":true}`,
	})
	c := New(srv.URL, "tlv_test")
	if !c.Healthy(context.Background()) {
		t.Error("Healthy = false, want true when Lens responds 200")
	}
}

func TestHealthy_ReturnsFalseWhenLensDown(t *testing.T) {
	// Closed server — connection refused.
	srv := mockLens(t, map[string]string{})
	srv.Close()

	c := New(srv.URL, "tlv_test")
	if c.Healthy(context.Background()) {
		t.Error("Healthy = true, want false when Lens unreachable")
	}
}

func TestNew_NotConfiguredReturnsSentinelError(t *testing.T) {
	c := New("", "")
	if c.IsConfigured() {
		t.Error("IsConfigured = true with empty URL")
	}
	if _, err := c.GetSpendSummary(context.Background(), "ws-1", 30); err != ErrNotConfigured {
		t.Errorf("expected ErrNotConfigured; got %v", err)
	}
}

// silence "unused import" warning while the file evolves
var _ = strings.Builder{}
