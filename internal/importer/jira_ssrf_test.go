package importer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// SEC-6 SSRF: the Jira importer fetches a user-supplied instance base URL. An attacker sets it to an
// internal address; without a guard the server fetches it. GREEN: the safe client refuses to dial the
// internal (loopback) address, so the importer never reaches it.
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
		t.Errorf("SSRF: fetchPage against an internal address returned nil error — want a blocked-address error")
	}
}
