package gatewayauth_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/gatewayauth"
)

const testSecret = "test-gateway-secret-0123456789" // >= 16 chars

// wire builds the middleware-wrapped handler and records what the downstream saw — so a
// test can prove identity reached (or did NOT reach) context.
func wire(exempt func(string) bool) (h http.Handler, called *bool, seen *gatewayauth.Identity) {
	called = new(bool)
	seen = new(gatewayauth.Identity)
	h = gatewayauth.Middleware(testSecret, exempt)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = true
		if id, ok := gatewayauth.IdentityFrom(r.Context()); ok {
			*seen = id
		}
		w.WriteHeader(http.StatusOK)
	}))
	return h, called, seen
}

func req(proof, email string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/issues", nil)
	if proof != "" {
		r.Header.Set(gatewayauth.HeaderGatewayAuth, proof)
	}
	if email != "" {
		r.Header.Set(gatewayauth.HeaderUserEmail, email)
	}
	return r
}

// TestAbsentProof_401_IdentityNotTrusted — no x-gateway-auth (an attacker setting
// x-user-email directly) → 401 BEFORE the handler; identity never reaches context.
func TestAbsentProof_401_IdentityNotTrusted(t *testing.T) {
	h, called, _ := wire(nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req("", "attacker@evil.com")) // identity header set, NO proof
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("absent proof = %d, want 401", rr.Code)
	}
	if *called {
		t.Fatal("downstream reached without a transit proof — a spoofed identity would be trusted")
	}
}

// TestWrongProof_401_IdentityNotTrusted — a forged x-gateway-auth → 401, handler unreached.
func TestWrongProof_401_IdentityNotTrusted(t *testing.T) {
	h, called, _ := wire(nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req("wrong-secret-but-長-enough-xxxx", "attacker@evil.com"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong proof = %d, want 401", rr.Code)
	}
	if *called {
		t.Fatal("downstream reached with a wrong proof")
	}
}

// TestValidProof_IdentityInContext — correct proof → request proceeds, verified email
// is in context.
func TestValidProof_IdentityInContext(t *testing.T) {
	h, called, seen := wire(nil)
	rr := httptest.NewRecorder()
	r := req(testSecret, "alice@corp.com")
	r.Header.Set(gatewayauth.HeaderUserID, "auth-sub-123")
	h.ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("valid proof = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !*called {
		t.Fatal("handler not reached on a valid proof")
	}
	if seen.Email != "alice@corp.com" || seen.UserID != "auth-sub-123" {
		t.Errorf("context identity = %+v, want email=alice@corp.com userid=auth-sub-123", *seen)
	}
}

// TestConstantTime_BothLengths401 — a wrong-but-SAME-length and a wrong-DIFFERENT-length
// proof both 401 identically; no length-based short-circuit is observable.
func TestConstantTime_BothLengths401(t *testing.T) {
	h, _, _ := wire(nil)
	for _, p := range []string{
		strings.Repeat("x", len(testSecret)),   // same length, wrong value
		"short",                                // different length, wrong value
		strings.Repeat("y", len(testSecret)*4), // much longer, wrong value
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req(p, ""))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("wrong proof (len %d) = %d, want 401", len(p), rr.Code)
		}
	}
}

// TestUsesConstantTimeCompare — guard that the secret compare is constant-time and never
// a plain ==.
func TestUsesConstantTimeCompare(t *testing.T) {
	src, err := os.ReadFile("gatewayauth.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "subtle.ConstantTimeCompare") {
		t.Error("secret compare must use subtle.ConstantTimeCompare")
	}
	for _, bad := range []string{"== secret", "secret ==", "Header.Get(HeaderGatewayAuth) =="} {
		if strings.Contains(s, bad) {
			t.Errorf("non-constant-time secret compare found: %q", bad)
		}
	}
}

// TestExemptPath_NoProofRequired — an own-auth path (HMAC webhook) passes through without
// a gateway proof (it doesn't rely on gateway identity).
func TestExemptPath_NoProofRequired(t *testing.T) {
	exempt := func(p string) bool { return strings.HasPrefix(p, "/v1/lens/webhook") }
	h := gatewayauth.Middleware(testSecret, exempt)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodPost, "/v1/lens/webhook", nil) // no proof
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("exempt path = %d, want 200 (own-auth path must not require the gateway proof)", rr.Code)
	}
}
