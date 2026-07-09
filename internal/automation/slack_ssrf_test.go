package automation

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// SEC-6 SSRF: the Slack notifier POSTs to a user-supplied webhook_url (from an automation rule). With
// no address guard, an attacker sets webhook_url to an internal address (cloud metadata 169.254.169.254,
// localhost, a cluster-internal service) and the server fetches it. RED (today): a loopback URL is
// reached. GREEN (post-fix): the safe HTTP client refuses to dial private/loopback/link-local ranges.
func TestSlack_SSRF_RefusesInternalAddress(t *testing.T) {
	var hits int32
	// httptest binds 127.0.0.1 — an "internal" (loopback) address standing in for a metadata/cluster svc.
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	// A verified caller's automation rule points webhook_url at the internal address.
	err := NewSlackNotifier().Send(internal.URL, "exfil", nil)

	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("SSRF: Slack notifier REACHED internal loopback %s (%d hits) — must be blocked before connect", internal.URL, hits)
	}
	if err == nil {
		t.Errorf("SSRF: Send to an internal address returned nil error — want a blocked-address error")
	}
}
