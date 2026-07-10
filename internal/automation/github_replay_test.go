package automation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// memDeduper is an in-memory deliveryDeduper for the handler test (production uses the DB-backed store).
type memDeduper struct{ seen map[string]bool }

func (m *memDeduper) Claim(_ context.Context, source, id string) (bool, error) {
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	k := source + ":" + id
	if m.seen[k] {
		return false, nil // already delivered → replay
	}
	m.seen[k] = true
	return true, nil
}

// SEC-7: GitHub re-delivers (and an attacker can re-POST) an identically-signed webhook body. The handler
// had no cross-delivery dedup (its only `seen` map is intra-payload), so each replay re-ran handleMerged —
// re-closing issues, re-commenting, re-firing automation. RED: a delivery POSTed twice with the same
// X-GitHub-Delivery is processed twice. GREEN: the second is a no-op (deduped on the delivery id).
func TestGitHub_ReplayedDeliveryIsDeduped(t *testing.T) {
	// A closed+merged PR referencing ENG-42 → handleMerged updates the issue to done (one side effect).
	body := []byte(`{"action":"closed","pull_request":{"number":7,"title":"Fixes ENG-42","body":"","merged":true}}`)
	fake := &fakeIssueLookup{issuesByIdentifier: map[string]*model.Issue{
		"ENG-42": {ID: "i-1", Identifier: "ENG-42", WorkspaceID: "ws-1"},
	}}
	h := NewGitHubHandler(nil, fake, "topsecret").WithDeduper(&memDeduper{})

	// Same signed body + same delivery id, delivered twice (GitHub retry / attacker replay).
	for i := 0; i < 2; i++ {
		req := signedGitHubReq(t, "topsecret", "pull_request", body)
		req.Header.Set("X-GitHub-Delivery", "12345678-abcd-0000-1111-222233334444")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i, w.Code)
		}
	}

	if n := len(fake.updates); n != 1 {
		t.Errorf("replayed delivery processed %d times — want 1 (cross-delivery dedup on X-GitHub-Delivery)", n)
	}
}
