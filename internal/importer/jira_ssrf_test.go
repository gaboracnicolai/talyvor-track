package importer

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/talyvor/track/internal/safehttp"
)

// SEC-6 SSRF: the Jira importer fetches a user-supplied instance base URL. An attacker sets it to an
// internal address; without a guard the server fetches it. GREEN: the safe client refuses to dial the
// internal (loopback) address, so the importer never reaches it.
//
// ⚠⚠ THIS FILE ASSERTED THE PAIR `hits == 0 && err != nil`, WHICH IS SATISFIED BY STRICTLY MORE THAN
// THE PROPERTY IT NAMES — AND THE LAST OF THE THREE SSRF TESTS TO STILL BE WRITTEN THAT WAY.
// `internal/automation/slack_ssrf_test.go` was repaired in #134 and `api_source_ssrf_test.go` has
// demanded the strong form since #132. MEASURED HERE at a0830e96, not read: replace this client's
// `clientOrSafe(httpc)` with one whose RoundTripper errors BEFORE it dials, and
// `go test -run TestJira_SSRF_RefusesInternalBaseURL ./internal/importer/` stays **GREEN** — hits==0
// because nothing dialled, err!=nil because the transport said so — while the byte-identical
// mutation reds `TestAPISources_SSRF_.../jira_api`, which asserts the error's IDENTITY. A guard that
// stops guarding but keeps failing is invisible to the old pair and visible to the new one.
//
// ⚠ THE CLASS WAS ALREADY COVERED FOR THIS TRANSPORT AND THAT IS WHY THIS IS A REPAIR AND NOT A
// DISCOVERY. `api_source_ssrf_test.go` sweeps `validAPISourceTypes` through `Runner.sourceFor`, so
// jira_api is swept. What was left was a per-file test whose assertion could not tell a refusal from
// a breakage — the shape #131/#132/#134 keep finding, sitting in the one file that names Jira.
func TestJira_SSRF_RefusesInternalBaseURL(t *testing.T) {
	var hits int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	c := newJiraClient("user@x.com:token", "PROJ", internal.URL)
	_, err := c.fetchPage(context.Background(), "")

	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("SSRF: Jira importer REACHED internal loopback %s (%d hits) — must be blocked", internal.URL, hits)
	}
	if err == nil {
		t.Fatalf("SSRF: fetchPage against an internal address returned nil error — want a blocked-address error")
	}
	if !errors.Is(err, safehttp.ErrBlockedAddress) {
		t.Errorf("SSRF: fetchPage refused with %v, not safehttp.ErrBlockedAddress. Reaching nothing is "+
			"not enough — a transport that is simply broken also reaches nothing and also errors. The "+
			"refusal must come from the address guard.", err)
	}
}

// The instance base URL an integration carries is a STRING the workspace controls, and a hostname
// that resolves to an internal address is the same attack as a literal one — this asserts the guard
// is keyed on the RESOLVED address rather than on the textual host, which is what safehttp promises
// ("the dialer resolves the target host"). It is the second case #134 added for Slack, applied to
// the sibling that never got it: without it, a guard narrowed to IP LITERALS keeps every other
// assertion in this file green. localhost keeps the test hermetic — no outbound DNS — while still
// going through the resolver path rather than the ParseIP path.
func TestJira_SSRF_RefusesAHostnameThatResolvesInternally(t *testing.T) {
	var hits int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	// The same server, addressed by NAME rather than by the 127.0.0.1 literal httptest hands out.
	_, port, err := net.SplitHostPort(strings.TrimPrefix(internal.URL, "http://"))
	if err != nil {
		t.Fatalf("could not split the httptest address %q: %v", internal.URL, err)
	}
	byName := "http://localhost:" + port

	c := newJiraClient("user@x.com:token", "PROJ", byName)
	_, fetchErr := c.fetchPage(context.Background(), "")

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("SSRF: Jira importer REACHED %s (%d hits) — a hostname resolving to loopback must be "+
			"refused exactly as the literal is", byName, got)
	}
	if !errors.Is(fetchErr, safehttp.ErrBlockedAddress) {
		t.Errorf("SSRF: fetchPage against %s refused with %v, not safehttp.ErrBlockedAddress", byName, fetchErr)
	}
}
