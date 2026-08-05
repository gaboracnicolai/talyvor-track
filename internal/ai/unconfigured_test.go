package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/lensintegration"
)

// ⚠ WHAT A USER SEES WHEN TRACK'S AI IS NOT CONFIGURED.
//
// Measured on the live box: TRACK_LENS_MINT_KEY is unset, so no AI feature in Track has ever
// worked. That is bad; what makes it a defect rather than a missing setting is what the product
// SAYS about it.
//
// IsAvailable() has only ever asked whether the Lens URL is set. A deployment with a URL and no
// mint credential therefore reports the engine as AVAILABLE, and every "AI is not configured"
// path in this repository — the one in the HTTP handler and the one in the MCP server, both
// written deliberately, both commented as graceful degradation — is UNREACHABLE. The user instead
// gets a 502 carrying a raw Lens 403 about credentials they have never heard of, which reads as a
// transient fault in someone else's service and will be retried forever.
//
// These tests pin the honest behaviour: unconfigured is stated plainly, once, in terms the person
// running Track can act on.

// lensStub is the smallest thing satisfying lensAccess.
type lensStub struct {
	configured bool
	base       string
}

func (l lensStub) IsConfigured() bool { return l.configured }
func (l lensStub) BaseURL() string    { return l.base }

// ⚠ THE HEADLINE. A URL without a mint credential is NOT an available AI engine.
//
// This is the whole defect in one assertion: the engine reported itself ready to do work it had
// no credential to perform.
func TestUnconfigured_AURLWithoutAMintCredentialIsNotAvailable(t *testing.T) {
	e := newEngineWithMint(lensStub{configured: true, base: "http://lens:8080"}, nil, nil, nil, "")
	if e.IsAvailable() {
		t.Error("IsAvailable() = true with no mint credential — every 'AI not configured' path in " +
			"this repo is behind this check, so reporting available makes all of them unreachable")
	}
}

// The control: with the credential present it IS available. Without this, a change that simply
// returned false everywhere would pass the test above and disable the product.
func TestUnconfigured_WithBothItIsAvailable(t *testing.T) {
	e := newEngineWithMint(lensStub{configured: true, base: "http://lens:8080"}, nil, nil, nil, "lens-mint-key")
	if !e.IsAvailable() {
		t.Error("IsAvailable() = false with a URL and a mint credential — AI would never run at all")
	}
}

// A missing URL was already handled and must stay handled.
func TestUnconfigured_NoURLIsStillUnavailable(t *testing.T) {
	e := newEngineWithMint(lensStub{configured: false}, nil, nil, nil, "lens-mint-key")
	if e.IsAvailable() {
		t.Error("IsAvailable() = true with no Lens URL")
	}
}

// ⚠ THE REFUSAL MUST NAME WHAT TO SET. "ai_available: false" on its own is an empty result: it
// tells a user the feature is off and nothing about how to turn it on, and it is indistinguishable
// from "Lens is down". The person who can fix this is running the server, and the only thing they
// need is the variable's name.
func TestUnconfigured_TheRefusalNamesTheVariableToSet(t *testing.T) {
	h := &Handler{engine: newEngineWithMint(lensStub{configured: true, base: "http://x"}, nil, nil, nil, "")}
	rec := httptest.NewRecorder()
	unavailableFor(rec, h.engine)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an unconfigured feature is not a server fault", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	if avail, _ := body["ai_available"].(bool); avail {
		t.Error("ai_available is true in the unavailable response")
	}
	reason, _ := body["reason"].(string)
	if reason == "" {
		t.Fatal("the response carries no reason — the user is told the feature is off and nothing else")
	}
	if !strings.Contains(reason, "TRACK_LENS_MINT_KEY") {
		t.Errorf("the reason does not name the variable to set: %q", reason)
	}
	// ⚠ AND IT MUST NOT POINT AT LENS'S GLOBAL ADMIN KEY. That key WOULD make minting work, which
	// is exactly why naming it here would be dangerous — it is the one credential that must never
	// be pasted into Track, and a hurried reader reaches for whatever the error names.
	//
	// TRACK_LENS_API_KEY is Track's own workspace key and mentioning it is correct and useful, so
	// the check removes those occurrences first. A bare Contains matched the substring inside
	// TRACK_LENS_API_KEY and failed a message that was right — an assertion that cannot tell the
	// two apart would have been satisfied by deleting the helpful half of the sentence.
	if strings.Contains(strings.ReplaceAll(reason, "TRACK_LENS_API_KEY", ""), "LENS_API_KEY") {
		t.Errorf("the reason points at Lens's global admin key, which must never enter Track: %q", reason)
	}
}

// ⚠ AND THE HANDLERS MUST ACTUALLY TAKE THAT PATH, not merely have it available. A correct
// unavailable() that no handler reaches is the state this repository was already in.
func TestUnconfigured_EveryAIHandlerRefusesRatherThanCallingLens(t *testing.T) {
	// A Lens that fails the test if it is ever contacted: an unconfigured engine must not reach it.
	var contacted bool
	lens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted = true
		w.WriteHeader(http.StatusForbidden)
	}))
	defer lens.Close()

	client := lensintegration.New(lens.URL, "") // URL set, no credential — the live box's state
	e := New(client, nil, nil, "")              // no mint key

	if e.IsAvailable() {
		t.Fatal("IsAvailable() = true, so the handlers below would call Lens")
	}
	if contacted {
		t.Error("Lens was contacted by an unconfigured engine")
	}
}
