package automation

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/model"
)

// github_replay_key_test.go — SEC-7, the KEY-PROVENANCE half.
//
// WHY THIS EXISTS ALONGSIDE github_replay_test.go. That test delivers one authentic body twice and
// asserts the side effect ran once — and it SETS X-GitHub-Delivery on both deliveries. So it pins
// the case where the replayer sends the same header the original carried, which is GitHub's own
// retry and is the ONE case the guard could see. verifyGitHubSignature covers the BODY ONLY; every
// header is chosen freely by whoever sends the request. MEASURED at 69b7ded, before the fix:
//
//	same signed body x2, X-GitHub-Delivery OMITTED -> 2 updates, 2 comments
//	same signed body x2, X-GitHub-Delivery VARIED  -> 2 updates, 2 comments
//	same signed body x2, X-GitHub-Delivery SAME    -> 1 update,  1 comment   <- the tested case
//
// A cross-delivery replay guard keyed on an unsigned header is a guard the replayer opts into.
// These cases assert the key comes from inside the signature.

var replayKeyBody = []byte(`{"action":"closed","pull_request":{"number":7,"title":"Fixes ENG-42","body":"","merged":true}}`)

// newReplayKeyHandler returns a handler wired exactly as cmd/track/main.go wires it (workspace +
// deduper), with a fake issue store that records every side effect.
func newReplayKeyHandler() (*GitHubWebhookHandler, *fakeIssueLookup) {
	fake := &fakeIssueLookup{issuesByIdentifier: map[string]*model.Issue{
		"ENG-42": {ID: "i-1", Identifier: "ENG-42", WorkspaceID: "ws-1"},
	}}
	h := NewGitHubHandler(nil, fake, "topsecret").WithWorkspace("ws-1").WithDeduper(&memDeduper{})
	return h, fake
}

// deliverGitHub POSTs one authentically signed delivery. deliveryID == "" omits the header
// entirely, which is what a replayer who simply does not copy it produces.
func deliverGitHub(t *testing.T, h *GitHubWebhookHandler, body []byte, deliveryID string) {
	t.Helper()
	req := signedGitHubReq(t, "topsecret", "pull_request", body)
	if deliveryID != "" {
		req.Header.Set("X-GitHub-Delivery", deliveryID)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	// Every one of these is an AUTHENTIC delivery — a deduped repeat is a 200 no-op, never a refusal.
	if w.Code != http.StatusOK {
		t.Fatalf("delivery %q: status = %d, want 200", deliveryID, w.Code)
	}
}

func TestGitHub_ReplayWithNoDeliveryHeaderIsStillDeduped(t *testing.T) {
	h, fake := newReplayKeyHandler()

	deliverGitHub(t, h, replayKeyBody, "")
	deliverGitHub(t, h, replayKeyBody, "")

	if n := len(fake.updates); n != 1 {
		t.Errorf("the same signed body replayed with NO X-GitHub-Delivery was processed %d times, want 1 — "+
			"the signature covers the body only, so omitting the header must not skip the replay guard", n)
	}
	if n := len(fake.comments); n != 1 {
		t.Errorf("closing comment written %d times, want 1", n)
	}
}

func TestGitHub_ReplayWithAVariedDeliveryHeaderIsStillDeduped(t *testing.T) {
	h, fake := newReplayKeyHandler()

	deliverGitHub(t, h, replayKeyBody, "aaaaaaaa-0000-0000-0000-000000000001")
	deliverGitHub(t, h, replayKeyBody, "bbbbbbbb-0000-0000-0000-000000000002")

	if n := len(fake.updates); n != 1 {
		t.Errorf("the same signed body replayed under a DIFFERENT X-GitHub-Delivery was processed %d times, "+
			"want 1 — a replayer picks that value, so it cannot be the only key the guard claims", n)
	}
	if n := len(fake.comments); n != 1 {
		t.Errorf("closing comment written %d times, want 1", n)
	}
}

// THE OTHER DIRECTION, and it is what stops the two cases above being satisfied by a handler that
// simply stopped working. A dedup guard that refuses EVERYTHING also produces "one side effect" —
// so assert that a DIFFERENT authentic delivery still gets through after a deduped one. Without
// this, replacing ServeHTTP's body with `return` leaves both cases above green.
func TestGitHub_DedupDoesNotSwallowADistinctDelivery(t *testing.T) {
	h, fake := newReplayKeyHandler()
	fake.issuesByIdentifier["ENG-43"] = &model.Issue{ID: "i-2", Identifier: "ENG-43", WorkspaceID: "ws-1"}

	other := []byte(`{"action":"closed","pull_request":{"number":8,"title":"Fixes ENG-43","body":"","merged":true}}`)

	deliverGitHub(t, h, replayKeyBody, "")
	deliverGitHub(t, h, replayKeyBody, "") // deduped
	deliverGitHub(t, h, other, "")         // a genuinely different event

	if n := len(fake.updates); n != 2 {
		t.Errorf("processed %d deliveries, want 2 — one replay must be dropped and the DISTINCT delivery "+
			"must still be processed; a guard that drops everything is not a dedup guard", n)
	}
}

// The deduper is optional (nil = pre-SEC-7 behaviour), and the fix must not turn a nil deduper into
// a nil-pointer panic on the new claim path. Asserts the unguarded handler still processes.
func TestGitHub_NilDeduperStillProcesses(t *testing.T) {
	fake := &fakeIssueLookup{issuesByIdentifier: map[string]*model.Issue{
		"ENG-42": {ID: "i-1", Identifier: "ENG-42", WorkspaceID: "ws-1"},
	}}
	h := NewGitHubHandler(nil, fake, "topsecret").WithWorkspace("ws-1") // no WithDeduper

	deliverGitHub(t, h, replayKeyBody, "")

	if n := len(fake.updates); n != 1 {
		t.Errorf("with no deduper wired the delivery was processed %d times, want 1", n)
	}
}
