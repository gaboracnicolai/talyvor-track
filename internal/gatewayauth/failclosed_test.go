package gatewayauth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/talyvor/track/internal/gatewayauth"
)

// AUDIT FINDING (fail-open shape): Middleware pre-hashed the secret and compared
// sha256(header) against sha256(secret). With an EMPTY secret those digests are
// sha256("") on both sides, so a request carrying NO x-gateway-auth header matched
// and every gateway-injected identity header downstream became "trusted". The whole
// authorization stack (membership, IDOR, owner gates) sits downstream of that compare.
//
// The only thing preventing it was a length check in a DIFFERENT package
// (internal/config). A length check is not the guard — the guard is REFUSING TO START.
// These tests pin that: the trust boundary itself must refuse to be constructed with a
// secret it cannot defend, so no future caller (a new binary, a test harness, a
// refactor that bypasses config.Load) can wire an open boundary.

// mustPanic runs fn and reports the recovered value; ok=false when fn returned normally.
func mustPanic(fn func()) (recovered any, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			recovered, ok = r, true
		}
	}()
	fn()
	return nil, false
}

// RED before the fix: Middleware("") returned a working middleware whose compare
// authorized a proofless request. GREEN after: constructing it panics.
func TestMiddleware_EmptySecret_RefusesToConstruct(t *testing.T) {
	if _, ok := mustPanic(func() { gatewayauth.Middleware("", nil) }); !ok {
		t.Fatal("Middleware(\"\") constructed successfully — the trust boundary must REFUSE to " +
			"start with an empty secret (sha256(\"\")==sha256(\"\") authorizes every proofless request)")
	}
}

// A secret shorter than the shared minimum is equally undefendable; the boundary owns
// that rule now, so it holds even if a caller never goes through config.Load.
func TestMiddleware_ShortSecret_RefusesToConstruct(t *testing.T) {
	short := "0123456789" // 10 < MinSecretLen
	if len(short) >= gatewayauth.MinSecretLen {
		t.Fatalf("test fixture is no longer short: len=%d min=%d", len(short), gatewayauth.MinSecretLen)
	}
	if _, ok := mustPanic(func() { gatewayauth.Middleware(short, nil) }); !ok {
		t.Fatalf("Middleware(%q) constructed successfully — a secret shorter than MinSecretLen (%d) must refuse to start",
			short, gatewayauth.MinSecretLen)
	}
}

// The exploit, expressed exactly as the audit forged it: no x-gateway-auth header,
// attacker-chosen x-user-email. With an empty secret this used to reach the handler
// with a "verified" identity. It must now be impossible to even build that middleware.
func TestEmptySecret_ProoflessRequestWithForgedIdentity_CannotBeServed(t *testing.T) {
	var served bool
	_, panicked := mustPanic(func() {
		h := gatewayauth.Middleware("", nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served = true
			if id, ok := gatewayauth.IdentityFrom(r.Context()); ok && id.Email != "" {
				t.Errorf("FORGED IDENTITY TRUSTED: downstream saw email=%q on a request with NO transit proof", id.Email)
			}
			w.WriteHeader(http.StatusOK)
		}))
		r := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
		r.Header.Set(gatewayauth.HeaderUserEmail, "victim@example.com")
		h.ServeHTTP(httptest.NewRecorder(), r)
	})
	if !panicked {
		t.Fatal("an empty-secret middleware was constructed and served a request — the forge path is still open")
	}
	if served {
		t.Fatal("handler ran on a proofless request under an empty secret")
	}
}

// Structural, independent of the secret: an EMPTY presented proof is rejected before
// any digest compare, so "both sides hash to sha256(\"\")" can never be the mechanism
// again even if the construction guard is one day relaxed.
func TestEmptyPresentedProof_AlwaysRejected(t *testing.T) {
	var served bool
	h := gatewayauth.Middleware(testSecret, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	r.Header.Set(gatewayauth.HeaderGatewayAuth, "") // explicitly empty, not absent
	r.Header.Set(gatewayauth.HeaderUserEmail, "victim@example.com")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if served {
		t.Fatal("handler ran on an empty x-gateway-auth")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("empty x-gateway-auth = %d, want 401", rr.Code)
	}
}
