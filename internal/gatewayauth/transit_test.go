package gatewayauth_test

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/gatewayauth"
)

// testdata/gateway-minted-assertion.json was minted by edge-infra's own signer
// (auth-service/src/transit.rs), so these tests verify what the real gateway sends.
type mintedFixture struct {
	Iat   int64           `json:"iat"`
	JWKS  json.RawMessage `json:"jwks"`
	Token string          `json:"token"`
}

const (
	fixtureHost   = "track.example.com"
	fixtureTarget = "/v1/workspaces?limit=5"
	fixtureSub    = "auth0|alice"
)

func loadMinted(t *testing.T) mintedFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/gateway-minted-assertion.json")
	if err != nil {
		t.Fatal(err)
	}
	var f mintedFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// verifierAt is a verifier holding the gateway's published JWKS, its clock at iat+offset.
func verifierAt(t *testing.T, f mintedFixture, offset time.Duration) *gatewayauth.TransitVerifier {
	t.Helper()
	keys, err := gatewayauth.ParseTransitJWKS(f.JWKS)
	if err != nil {
		t.Fatal(err)
	}
	v := gatewayauth.NewTransitVerifier(keys, "")
	gatewayauth.SetTransitClock(v, func() time.Time { return time.Unix(f.Iat, 0).Add(offset) })
	return v
}

func mintedRequest(f mintedFixture) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+fixtureHost+fixtureTarget, nil)
	r.Header.Set(gatewayauth.HeaderGatewayAuth, f.Token)
	r.Header.Set(gatewayauth.HeaderUserID, fixtureSub)
	r.Header.Set(gatewayauth.HeaderUserEmail, "mallory@evil.com") // not trusted: the email comes from the signed claims
	return r
}

func TestTransit_GatewayMintedAssertion_AcceptedOnce_ReplayRefused(t *testing.T) {
	f := loadMinted(t)
	var calls int
	var seen gatewayauth.Identity
	h := gatewayauth.TransitMiddleware(verifierAt(t, f, time.Second), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		seen, _ = gatewayauth.IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, mintedRequest(f))
	if first.Code != http.StatusOK {
		t.Fatalf("fresh gateway-minted assertion = %d, want 200; body=%s", first.Code, first.Body.String())
	}
	want := gatewayauth.Identity{Email: "alice@corp.com", UserID: fixtureSub, Teams: "eng,ops", Issuer: "https://idp.example.com/"}
	if seen != want {
		t.Errorf("identity in context = %+v, want %+v (from the signed claims, not the headers)", seen, want)
	}

	replay := httptest.NewRecorder()
	h.ServeHTTP(replay, mintedRequest(f))
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed assertion = %d, want 401", replay.Code)
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1 — a captured assertion was accepted twice", calls)
	}
}

func TestTransit_ExpiredAssertion_Refused(t *testing.T) {
	f := loadMinted(t)
	// exp = iat+30; the leeway is 5s, so iat+36 is past it.
	_, err := verifierAt(t, f, 36*time.Second).Verify(f.Token, http.MethodGet, fixtureHost, fixtureTarget, fixtureSub)
	if !errors.Is(err, gatewayauth.ErrTransitExpired) {
		t.Fatalf("expired assertion: err = %v, want %v", err, gatewayauth.ErrTransitExpired)
	}
}

func TestTransit_AssertionForAnotherRequest_Refused(t *testing.T) {
	f := loadMinted(t)
	_, err := verifierAt(t, f, time.Second).Verify(f.Token, http.MethodDelete, fixtureHost, "/v1/workspaces/ws-1", fixtureSub)
	if !errors.Is(err, gatewayauth.ErrTransitWrongRequest) {
		t.Fatalf("assertion presented on another request: err = %v, want %v", err, gatewayauth.ErrTransitWrongRequest)
	}
}

func TestTransit_SubMustEqualXUserID(t *testing.T) {
	f := loadMinted(t)
	_, err := verifierAt(t, f, time.Second).Verify(f.Token, http.MethodGet, fixtureHost, fixtureTarget, "auth0|mallory")
	if !errors.Is(err, gatewayauth.ErrTransitWrongUser) {
		t.Fatalf("x-user-id differs from sub: err = %v, want %v", err, gatewayauth.ErrTransitWrongUser)
	}
}

func TestTransit_TamperedClaims_BadSignature(t *testing.T) {
	f := loadMinted(t)
	parts := strings.Split(f.Token, ".")
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	forged := strings.Replace(string(claims), "alice@corp.com", "admin@corp.com", 1)
	token := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(forged)) + "." + parts[2]
	_, err := verifierAt(t, f, time.Second).Verify(token, http.MethodGet, fixtureHost, fixtureTarget, fixtureSub)
	if !errors.Is(err, gatewayauth.ErrTransitBadSignature) {
		t.Fatalf("tampered claims: err = %v, want %v", err, gatewayauth.ErrTransitBadSignature)
	}
}

func TestTransit_KeyFromJWKSURL(t *testing.T) {
	f := loadMinted(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/transit-jwks.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(f.JWKS)
	}))
	defer srv.Close()

	v := gatewayauth.NewTransitVerifier(gatewayauth.NewJWKSURLKeys(srv.URL+"/.well-known/transit-jwks.json", srv.Client()), "")
	gatewayauth.SetTransitClock(v, func() time.Time { return time.Unix(f.Iat+1, 0) })
	if _, err := v.Verify(f.Token, http.MethodGet, fixtureHost, fixtureTarget, fixtureSub); err != nil {
		t.Fatalf("assertion verified against the fetched JWKS: %v", err)
	}
}

// A PEM public key gets the kid the gateway gives it, so an operator can configure
// `openssl pkey -pubout` output instead of the JWKS.
func TestTransit_PEMPublicKey_GetsTheGatewaysKid(t *testing.T) {
	f := loadMinted(t)
	jwks, _ := gatewayauth.ParseTransitJWKS(f.JWKS)
	pub := jwks["ePr3pXKmgtJDhC6W"]
	der, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(pub))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := gatewayauth.ParseTransitKeys(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["ePr3pXKmgtJDhC6W"]; !ok {
		t.Fatalf("PEM key parsed under kids %v, want the gateway's kid ePr3pXKmgtJDhC6W", keys)
	}
}
