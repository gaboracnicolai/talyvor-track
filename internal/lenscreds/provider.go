// Package lenscreds mints and caches per-workspace Talyvor Lens JWTs.
//
// Track authenticates to Lens with ONE shared admin key. If that key
// rode Track's LLM/embedding data-path calls, Lens would resolve every
// tenant's workspace to EMPTY: one shared rate-limit bucket (a 429 in
// tenant A throttles tenant B) and all spend attributed to "default"
// (per-tenant COGS blind). The cure — mirrored from talyvor-docs'
// internal/lenscreds — is to spend the admin key ONLY on the admin-gated
// mint endpoint (POST /v1/auth/token) and put a short-lived,
// per-workspace JWT on every data-path call. Lens then meters, rate-
// limits, and attributes each call against the workspace in the token's
// claim.
//
// The provider caches one JWT per workspace, refreshes before expiry,
// coalesces concurrent mints for the same workspace behind a per-entry
// lock (while different workspaces mint concurrently), and NEVER returns
// the admin key: a mint failure returns an error so the caller fails
// closed instead of silently re-collapsing attribution onto the shared
// key.
package lenscreds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// tokenTTLHours is the lifetime Track requests for each per-workspace
// JWT. refreshSkew is how long BEFORE expiry the provider re-mints, so
// an in-flight request never rides a token that expires mid-call.
const (
	tokenTTLHours = 12
	refreshSkew   = 30 * time.Minute
)

const mintPath = "/v1/auth/token"

// entry is one workspace's cached token. Its own mutex serializes mints
// for that workspace (so concurrent callers coalesce onto a single mint)
// without blocking other workspaces, which hold different entries.
type entry struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// Provider mints and caches one per-workspace Lens JWT per workspace,
// using the shared ADMIN key ONLY as the minting credential. It never
// hands the admin key to a data path.
type Provider struct {
	lensURL    string
	adminKey   string
	httpClient *http.Client
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

// New returns a Provider that mints against lensURL using adminKey.
func New(lensURL, adminKey string) *Provider {
	return &Provider{
		lensURL:    lensURL,
		adminKey:   adminKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		now:        time.Now,
		entries:    make(map[string]*entry),
	}
}

// entryFor returns the cache entry for a workspace, creating it once.
// Guarded by p.mu so the get-or-create is atomic; the returned *entry is
// stable for the workspace's lifetime, which is what lets same-workspace
// mints coalesce on entry.mu.
func (p *Provider) entryFor(workspaceID string) *entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[workspaceID]
	if !ok {
		e = &entry{}
		p.entries[workspaceID] = e
	}
	return e
}

// TokenFor returns a per-workspace JWT for workspaceID, minting a fresh
// one when the cache is empty or the cached token is within refreshSkew
// of expiry. On a mint failure it returns an error and never the admin
// key — the caller must fail closed.
func (p *Provider) TokenFor(ctx context.Context, workspaceID string) (string, error) {
	e := p.entryFor(workspaceID)
	// Hold the per-entry lock across the mint so concurrent callers for
	// THIS workspace coalesce: the first mints, the rest observe the
	// freshly cached token. Different workspaces hold different entries,
	// so they mint concurrently.
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.token != "" && p.now().Before(e.expiresAt.Add(-refreshSkew)) {
		return e.token, nil
	}

	tok, exp, err := p.mint(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	e.token = tok
	e.expiresAt = exp
	return tok, nil
}

// mintResponse is the admin-mint contract: POST /v1/auth/token →
// 201 {"token","expires_at"}.
type mintResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// mint calls the admin-gated Lens mint endpoint with the shared admin
// key and returns the freshly issued per-workspace token and its expiry.
func (p *Provider) mint(ctx context.Context, workspaceID string) (string, time.Time, error) {
	reqBody, err := json.Marshal(map[string]any{
		"workspace_id": workspaceID,
		"ttl_hours":    tokenTTLHours,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.lensURL+mintPath, bytes.NewReader(reqBody))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.adminKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("lenscreds: mint workspace %s: %w", workspaceID, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("lenscreds: mint workspace %s: Lens returned %d: %s", workspaceID, resp.StatusCode, string(raw))
	}
	var out mintResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("lenscreds: mint workspace %s: decode response: %w", workspaceID, err)
	}
	if out.Token == "" {
		return "", time.Time{}, fmt.Errorf("lenscreds: mint workspace %s: Lens returned an empty token", workspaceID)
	}
	exp := out.ExpiresAt
	if exp.IsZero() {
		// Defensive: if Lens omits expires_at, fall back to the TTL we
		// asked for so the refresh logic still has a horizon.
		exp = p.now().Add(tokenTTLHours * time.Hour)
	}
	return out.Token, exp, nil
}
