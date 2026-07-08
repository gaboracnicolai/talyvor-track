package importer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/talyvor/track/internal/safehttp"
)

// providerhttp.go — T8 Build C.3: the small POST-JSON helper the Linear/Jira clients share. The
// lensintegration do() is GET-only + Lens-specific, so this is a purpose-built helper in the house style
// (context, timeout, JSON body). Rate-limit interpretation differs per provider (Linear 400/RATELIMITED body
// vs Jira 429/Retry-After), so each client owns its own retry loop; this file holds only the shared plumbing.

// errRateLimited is a DISTINCT, typed signal — a rate-limit give-up is not a generic failure. Tests + callers
// can errors.Is() it to tell "provider throttled us" apart from "auth revoked / malformed response".
var errRateLimited = errors.New("provider: rate limited")

const (
	defaultMaxAttempts    = 3
	maxRateLimitBackoff   = 30 * time.Second
	defaultRequestTimeout = 20 * time.Second
)

// clientOrSafe returns the first non-nil injected client, or the SSRF-guarded client by default.
// SEC-6: every provider fetch of a user-supplied instance URL goes through the guarded client so it
// cannot be pointed at an internal address; tests inject a plain client to reach a loopback httptest.
func clientOrSafe(override []*http.Client) *http.Client {
	if len(override) > 0 && override[0] != nil {
		return override[0]
	}
	return safehttp.Client(defaultRequestTimeout)
}

// retryer carries the retry knobs. sleep is injectable so tests run without real waits.
type retryer struct {
	maxAttempts int
	sleep       func(time.Duration)
}

func defaultRetryer() retryer {
	return retryer{maxAttempts: defaultMaxAttempts, sleep: time.Sleep}
}

func (r retryer) attempts() int {
	if r.maxAttempts <= 0 {
		return defaultMaxAttempts
	}
	return r.maxAttempts
}

func (r retryer) wait(d time.Duration) {
	if d <= 0 {
		return
	}
	if d > maxRateLimitBackoff {
		d = maxRateLimitBackoff
	}
	s := r.sleep
	if s == nil {
		s = time.Sleep
	}
	s(d)
}

// postJSON issues one POST with the given headers and body, returning the status, response headers, and the
// fully-read body. Header mutation is left to the caller (each provider sets its own auth).
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body []byte) (status int, respHeaders http.Header, respBody []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, b, nil
}

// parseRetryAfter reads a Retry-After header (Jira) as a seconds count. Absent/invalid ⇒ a small default so
// the client still backs off rather than hot-looping.
func parseRetryAfter(h http.Header, fallback time.Duration) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return fallback
}
