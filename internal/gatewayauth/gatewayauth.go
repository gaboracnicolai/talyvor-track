// Package gatewayauth is Track's root-of-trust auth boundary (T9). The edge gateway
// (edge-infra Envoy ext_authz) validates a Bearer JWT and injects gateway-verified
// identity headers plus a transit-proof header, x-gateway-auth, carrying a shared
// secret. A direct caller bypassing the gateway can set the identity headers freely but
// cannot know the secret — so the identity headers are trustworthy ONLY on a request
// whose x-gateway-auth proves it transited the gateway.
//
// This package verifies that proof (constant-time) and, only then, lifts the verified
// identity into the request context. T9 is the boundary only: it does NOT enforce
// membership or scope the store to a workspace (that is T10) — it guarantees that
// nothing downstream can read a trusted identity unless the proof verified.
package gatewayauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// Header names. HTTP header lookup is case-insensitive (canonicalized by net/http), so
// the gateway's lowercase x-… forms resolve through these canonical names.
const (
	HeaderGatewayAuth = "X-Gateway-Auth" // transit proof (the shared secret)
	HeaderUserEmail   = "X-User-Email"   // JWT email claim — the workspace-member join key (T10)
	HeaderUserID      = "X-User-Id"      // JWT sub — auth-system user id (NOT Track's member.id)
	HeaderUserTeams   = "X-User-Teams"   // JWT teams claim, comma-separated
	HeaderAuthIss     = "X-Auth-Iss"     // JWT issuer
)

// Identity is the gateway-verified caller identity. It is placed in context ONLY after
// the transit proof verifies. Fields may be empty if the JWT lacked the claim — T9 does
// not require any of them to be present (that enforcement is T10); it only guarantees
// that whatever is here came through the gateway.
type Identity struct {
	Email  string
	UserID string
	Teams  string
	Issuer string
}

type ctxKey struct{}

// WithIdentity returns a context carrying the verified identity.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// IdentityFrom returns the verified identity, ok=false if none (i.e. the request did not
// pass the transit-proof boundary). Downstream code that needs a trusted identity must
// treat ok=false as unauthenticated.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Middleware verifies the transit proof and, only on success, lifts the gateway identity
// into context. exempt(path) returns true for routes that authenticate by their own
// mechanism (HMAC webhooks, anonymous public boards, guest tokens, the websocket) and so
// must NOT require the gateway proof; a nil exempt protects every route.
//
// On a non-exempt request: x-gateway-auth is compared to the secret CONSTANT-TIME (over
// SHA-256 digests, so there is no length-dependent path at all). Absent or mismatched →
// 401 immediately, BEFORE any identity header is read. The identity is read and trusted
// only after the proof verifies.
func Middleware(secret string, exempt func(path string) bool) func(http.Handler) http.Handler {
	// Pre-hash the secret once; the per-request compare is over two fixed-length digests.
	secretDigest := sha256.Sum256([]byte(secret))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt != nil && exempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Transit proof FIRST — nothing identity-related is read until this passes.
			proofDigest := sha256.Sum256([]byte(r.Header.Get(HeaderGatewayAuth)))
			if subtle.ConstantTimeCompare(proofDigest[:], secretDigest[:]) != 1 {
				unauthorized(w)
				return
			}

			// Proof verified → the gateway-injected identity headers are trustworthy.
			id := Identity{
				Email:  r.Header.Get(HeaderUserEmail),
				UserID: r.Header.Get(HeaderUserID),
				Teams:  r.Header.Get(HeaderUserTeams),
				Issuer: r.Header.Get(HeaderAuthIss),
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"missing or invalid gateway transit proof","code":"GATEWAY_AUTH_REQUIRED"}`))
}
