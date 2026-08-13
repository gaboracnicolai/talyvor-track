package automation

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/talyvor/track/internal/safehttp"
)

// SEC-6 SSRF: the Slack notifier POSTs to a user-supplied webhook_url (from an automation rule). With
// no address guard, an attacker sets webhook_url to an internal address (cloud metadata 169.254.169.254,
// localhost, a cluster-internal service) and the server fetches it. RED (today): a loopback URL is
// reached. GREEN (post-fix): the safe HTTP client refuses to dial private/loopback/link-local ranges.
//
// ⚠ WHY THERE IS AN ERROR-IDENTITY ASSERTION AND NOT JUST "no hits and a non-nil error".
// This test shipped as `hits == 0 && err != nil`, and that pair is satisfied by strictly MORE than
// the property the test names — most importantly by a Slack notifier that has no address guard at
// all and simply never dials. MEASURED at `296cf095`, not argued: replacing this package's
// production transport with a RoundTripper that returns an error before connecting
//
//	return &SlackNotifier{httpClient: &http.Client{Transport: deadTransport{}}}   // never dials
//
// left `go test -run TestSlack_SSRF ./internal/automation/` GREEN. The one test in the repository
// whose entire job is to prove the Slack webhook cannot be pointed at 169.254.169.254 could not
// tell "the address guard refused" from "the transport is dead". Positive control on the same
// harness: the crude regression — swapping safehttp.Client for a plain &http.Client{} — DID go red
// on the hits counter, so the old shape was blind to one specific failure mode rather than inert.
//
// `errors.Is(err, safehttp.ErrBlockedAddress)` is therefore the load-bearing assertion: it can only
// be satisfied by a refusal that came OUT OF THE ADDRESS GUARD. This is the same reasoning, and the
// same assertion, that internal/importer/api_source_ssrf_test.go applies to the *_api transports;
// Slack is the third outbound fetcher of a caller-supplied URL and was the one left on the weak
// shape. (The three plain &http.Client{}s in lensintegration, lenscreds and ai/engine are NOT in
// this population: every one targets TRACK_LENS_URL, an operator-set env var, and a self-hosted
// Lens on a private address is the intended deployment rather than an SSRF.)
func TestSlack_SSRF_RefusesInternalAddress(t *testing.T) {
	var hits int32
	// httptest binds 127.0.0.1 — an "internal" (loopback) address standing in for a metadata/cluster svc.
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	// A verified caller's automation rule points webhook_url at the internal address.
	// NewSlackNotifier takes no client — what is exercised is the transport production builds.
	err := NewSlackNotifier().Send(internal.URL, "exfil", nil)

	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("SSRF: Slack notifier REACHED internal loopback %s (%d hits) — must be blocked before connect", internal.URL, hits)
	}
	if err == nil {
		t.Fatalf("SSRF: Send to an internal address returned nil error — want a blocked-address error")
	}
	if !errors.Is(err, safehttp.ErrBlockedAddress) {
		t.Errorf("SSRF: Send refused with %v, not safehttp.ErrBlockedAddress. Reaching nothing is not "+
			"enough — a transport that is simply broken also reaches nothing and also errors. The "+
			"refusal must come from the address guard.", err)
	}
}

// The webhook URL an automation rule carries is a STRING the caller controls, and a hostname that
// resolves to an internal address is the same attack as a literal one — this asserts the guard is
// keyed on the RESOLVED address rather than on the textual host, which is what safehttp promises
// ("the dialer resolves the target host"). localtest.me and its ilk are public DNS names that
// resolve to 127.0.0.1; using localhost keeps the test hermetic — it needs no outbound DNS — while
// still going through the resolver path rather than the ParseIP path.
func TestSlack_SSRF_RefusesAHostnameThatResolvesInternally(t *testing.T) {
	var hits int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	// Same server, addressed by NAME rather than by the 127.0.0.1 literal httptest hands out.
	_, port, err := net.SplitHostPort(strings.TrimPrefix(internal.URL, "http://"))
	if err != nil {
		t.Fatalf("could not split the httptest address %q: %v", internal.URL, err)
	}
	byName := "http://localhost:" + port

	sendErr := NewSlackNotifier().Send(byName, "exfil", nil)

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("SSRF: Slack notifier REACHED %s (%d hits) — a hostname resolving to loopback must "+
			"be refused exactly as the literal is", byName, got)
	}
	if !errors.Is(sendErr, safehttp.ErrBlockedAddress) {
		t.Errorf("SSRF: Send to %s refused with %v, not safehttp.ErrBlockedAddress", byName, sendErr)
	}
}
