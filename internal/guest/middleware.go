package guest

import (
	"context"
	"net/http"
	"strings"
)

// ctxKey is the unexported context-key type used to stash the guest
// claims on the request context. Unexported so external packages
// must use Claims(ctx) to read it — that keeps the surface tiny.
type ctxKey struct{}

// Claims pulls the guest claims off the request context, or nil if
// the request isn't authenticated as a guest. Handlers branch on
// the nil result to decide whether to serve guest-scoped data.
func Claims(ctx context.Context) *GuestClaims {
	v, _ := ctx.Value(ctxKey{}).(*GuestClaims)
	return v
}

// Middleware verifies a Bearer guest token (when present) and
// stashes the decoded claims on the request context. Missing /
// malformed tokens are NOT a hard fail — downstream handlers decide
// whether guest access is required, so this middleware is composable
// with regular member auth (which checks a different header).
//
// "Members always use member auth, not guest tokens" — the
// middleware only treats Bearer tokens shaped like our two-part
// signed payload as guest tokens. Member API keys (which use a
// different prefix in practice) are passed through untouched.
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" || !looksLikeGuestToken(tok) {
			next.ServeHTTP(w, r)
			return
		}
		claims, err := s.VerifyToken(tok)
		if err != nil {
			// Bad signature → continue without claims. The handler
			// will refuse access if it required them; member-token
			// requests aren't impacted.
			next.ServeHTTP(w, r)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireGuest is a tighter middleware that 401s any request lacking
// a valid guest claim. Use it on the public /v1/guest/* routes where
// a guest token is mandatory.
func (s *Store) RequireGuest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Claims(r.Context()) == nil {
			http.Error(w, `{"error":"guest token required"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AllowWrite gates write-style guest requests by role. Returns true
// if the claim permits the action. Callers wrap their POST/PATCH
// branches in `if !AllowWrite(claims, action) { 403 }`.
//
//   - "comment": commenter or editor
//   - "edit":    editor only
func AllowWrite(claims *GuestClaims, action string) bool {
	if claims == nil {
		return false
	}
	switch action {
	case "comment":
		return claims.Role == GuestRoleCommenter || claims.Role == GuestRoleEditor
	case "edit":
		return claims.Role == GuestRoleEditor
	}
	return false
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

// looksLikeGuestToken does a cheap shape check so the middleware can
// skip member tokens without paying for the HMAC compare. Guest
// tokens are exactly `payload.signature` (two parts, both base64-
// url, no other separators).
func looksLikeGuestToken(t string) bool {
	parts := strings.Split(t, ".")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}
