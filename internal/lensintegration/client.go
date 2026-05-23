// Package lensintegration is the Talyvor Track ↔ Talyvor Lens bridge.
//
// Two flows:
//
//  1. Outbound (Client): Track polls Lens every 15 minutes for the
//     per-feature spend breakdown and accumulates the cost on issues
//     whose `lens_feature` matches the feature name.
//
//  2. Inbound (Webhook): when Lens crosses a configured spend
//     threshold for a feature it POSTs to Track, which records the
//     event, notifies the assignee, and broadcasts to subscribers.
//
// The Client is the read side; Syncer + Webhook are the write side.
package lensintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	lensURL    string
	apiKey     string
	httpClient *http.Client
}

// SpendSummary mirrors the Lens /v1/api/spend/summary response shape.
// Track doesn't need every field Lens emits — we project to the
// columns that matter for the AI-costs dashboard.
type SpendSummary struct {
	TotalCostUSD      float64 `json:"total_cost_usd"`
	TotalInputTokens  int     `json:"total_input_tokens"`
	TotalOutputTokens int     `json:"total_output_tokens"`
	TotalRequests     int     `json:"total_requests"`
	CacheHitRate      float64 `json:"cache_hit_rate"`
	AvgCostPerRequest float64 `json:"avg_cost_per_request"`
	PeriodDays        int     `json:"period_days"`
}

// FeatureSpend is the Lens /v1/api/spend/by-feature row shape. The
// Feature field carries whatever value the caller set in the
// X-Talyvor-Feature header — Track convention is the issue
// identifier ("ENG-42") so the sync step can join cleanly.
type FeatureSpend struct {
	Feature      string  `json:"feature"`
	CostUSD      float64 `json:"cost_usd"`
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
}

// New returns a Client targeting the given Lens URL. An empty URL is
// allowed — every method returns ErrNotConfigured so callers can
// detect "no Lens deployed" without crashing.
func New(lensURL, apiKey string) *Client {
	return &Client{
		lensURL:    lensURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// ErrNotConfigured signals that the Lens integration is dormant —
// usually because TRACK_LENS_URL wasn't set. Handlers translate this
// to a graceful "lens_configured: false" response, not a 500.
var ErrNotConfigured = errors.New("lensintegration: Lens URL not configured")

// IsConfigured reports whether the client has enough state to make a
// request. Tests of Track-only behaviour can construct a zero Client
// and rely on this returning false everywhere.
func (c *Client) IsConfigured() bool { return c != nil && c.lensURL != "" }

// BaseURL returns the configured Lens URL. Used by the AI engine to
// build its own POST requests to the Lens proxy endpoints (which
// aren't part of this Client's GET-only API).
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.lensURL
}

// APIKey returns the configured Lens API key. The AI engine sets it
// as the Authorization header on every proxy call.
func (c *Client) APIKey() string {
	if c == nil {
		return ""
	}
	return c.apiKey
}

func (c *Client) do(ctx context.Context, path string, out any) error {
	if !c.IsConfigured() {
		return ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.lensURL+path, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("lens: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("lens: GET %s returned %d: %s", path, resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (c *Client) GetSpendSummary(ctx context.Context, workspaceID string, days int) (*SpendSummary, error) {
	if days <= 0 {
		days = 30
	}
	q := url.Values{}
	q.Set("workspace_id", workspaceID)
	q.Set("days", strconv.Itoa(days))
	var out SpendSummary
	if err := c.do(ctx, "/v1/api/spend/summary?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetSpendByFeature(ctx context.Context, workspaceID string, days int) ([]FeatureSpend, error) {
	if days <= 0 {
		days = 30
	}
	q := url.Values{}
	q.Set("workspace_id", workspaceID)
	q.Set("days", strconv.Itoa(days))
	var out []FeatureSpend
	if err := c.do(ctx, "/v1/api/spend/by-feature?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetAnomalies(ctx context.Context, workspaceID string) ([]map[string]any, error) {
	q := url.Values{}
	q.Set("workspace_id", workspaceID)
	var out []map[string]any
	if err := c.do(ctx, "/v1/api/anomalies?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Healthy returns true when Lens responds OK to /v1/api/health.
// Returns false on any error, timeout, or non-2xx status.
func (c *Client) Healthy(ctx context.Context) bool {
	if !c.IsConfigured() {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.lensURL+"/v1/api/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}
