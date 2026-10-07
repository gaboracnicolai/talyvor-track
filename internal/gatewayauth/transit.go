package gatewayauth

// Signed transit assertions (B28.442).
//
// edge-infra's auth-service no longer puts a shared secret in x-gateway-auth. It signs a
// short-lived EdDSA JWT for each request it lets through — naming the request (htm, htu =
// host + path), the identity it vouches for (sub, email, teams) and a jti — and publishes its
// public key as a JWKS at auth-service:9090/.well-known/transit-jwks.json. Track verifies it
// with that public key, so nothing Track holds can mint one. The rules mirror the reference
// verifier, edge-infra auth-service/src/transit.rs: known kid, valid Ed25519 signature, right
// issuer, not expired, lifetime at most 60s, minted for THIS method + host + path, and each
// jti accepted once.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTransitIssuer is auth-service's TRANSIT_ISSUER default.
	DefaultTransitIssuer = "edge-gateway"
	// TransitLeewaySeconds is the clock skew tolerated between the gateway and Track.
	TransitLeewaySeconds = 5
	// TransitMaxLifetimeSeconds bounds exp - iat whatever the signer is set to, and with it
	// the replay window and the size of the jti cache.
	TransitMaxLifetimeSeconds = 60
)

// Why an assertion was refused. Every one of them is a 401 to the caller.
var (
	ErrTransitMalformed    = errors.New("malformed assertion")
	ErrTransitUnknownKey   = errors.New("assertion signed by an unknown key")
	ErrTransitBadSignature = errors.New("bad signature")
	ErrTransitExpired      = errors.New("assertion expired")
	ErrTransitWrongIssuer  = errors.New("assertion from the wrong issuer")
	ErrTransitTooLong      = errors.New("assertion lives longer than 60s")
	ErrTransitWrongRequest = errors.New("assertion was minted for a different request")
	ErrTransitWrongUser    = errors.New("assertion vouches for a different user than x-user-id")
	ErrTransitReplayed     = errors.New("assertion already used")
)

// TransitClaims are the claims auth-service signs.
type TransitClaims struct {
	Iss   string   `json:"iss"`
	Sub   string   `json:"sub"`
	Amr   string   `json:"amr"` // jwt, mtls or agent
	Idp   string   `json:"idp,omitempty"`
	Email string   `json:"email,omitempty"`
	Teams []string `json:"teams,omitempty"`
	Htm   string   `json:"htm"`
	Htu   string   `json:"htu"`
	Iat   int64    `json:"iat"`
	Exp   int64    `json:"exp"`
	Jti   string   `json:"jti"`
}

// TransitKeys resolves a kid to the gateway's Ed25519 public key.
type TransitKeys interface {
	Key(kid string) (ed25519.PublicKey, bool)
}

// StaticTransitKeys is a fixed set of keys, from TRACK_TRANSIT_JWKS.
type StaticTransitKeys map[string]ed25519.PublicKey

func (s StaticTransitKeys) Key(kid string) (ed25519.PublicKey, bool) {
	k, ok := s[kid]
	return k, ok
}

// TransitKID is the kid auth-service gives a key: base64url of the first 12 bytes of
// sha256(public key).
func TransitKID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// ParseTransitKeys reads the gateway's public key as either the JWKS document auth-service
// publishes or a PEM public key (`openssl pkey -pubout`).
func ParseTransitKeys(s string) (StaticTransitKeys, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		return ParseTransitJWKS([]byte(s))
	}
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("neither a JWKS document nor a PEM public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("PEM public key: %w", err)
	}
	ed, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("PEM public key is %T, not Ed25519", pub)
	}
	return StaticTransitKeys{TransitKID(ed): ed}, nil
}

// ParseTransitJWKS reads the Ed25519 keys out of a JWKS document, skipping any other kind.
func ParseTransitJWKS(b []byte) (StaticTransitKeys, error) {
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Kid string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("JWKS: %w", err)
	}
	keys := StaticTransitKeys{}
	for _, k := range doc.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Kid == "" {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("JWKS: transit key %s: x is not a base64url Ed25519 public key", k.Kid)
		}
		keys[k.Kid] = ed25519.PublicKey(x)
	}
	if len(keys) == 0 {
		return nil, errors.New("JWKS: no Ed25519 transit key")
	}
	return keys, nil
}

// JWKSURLKeys fetches the gateway's JWKS from TRACK_TRANSIT_JWKS_URL and fetches it again
// when an assertion names a kid it does not hold — which is what a key rotation looks like
// from here. Re-fetches are at most one per refreshEvery, so a flood of made-up kids cannot
// turn into a flood of requests to the gateway.
type JWKSURLKeys struct {
	url          string
	client       *http.Client
	refreshEvery time.Duration

	mu        sync.Mutex
	keys      StaticTransitKeys
	lastFetch time.Time
}

func NewJWKSURLKeys(url string, client *http.Client) *JWKSURLKeys {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &JWKSURLKeys{url: url, client: client, refreshEvery: 10 * time.Second}
}

func (j *JWKSURLKeys) Key(kid string) (ed25519.PublicKey, bool) {
	j.mu.Lock()
	if k, ok := j.keys[kid]; ok {
		j.mu.Unlock()
		return k, true
	}
	if !j.lastFetch.IsZero() && time.Since(j.lastFetch) < j.refreshEvery {
		j.mu.Unlock()
		return nil, false
	}
	j.lastFetch = time.Now()
	j.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := j.Refresh(ctx); err != nil {
		slog.Warn("gatewayauth: transit JWKS fetch failed", "url", j.url, "err", err)
		return nil, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	k, ok := j.keys[kid]
	return k, ok
}

// Refresh fetches the JWKS now and replaces the keys held.
func (j *JWKSURLKeys) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return err
	}
	resp, err := j.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", j.url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	keys, err := ParseTransitJWKS(body)
	if err != nil {
		return err
	}
	j.mu.Lock()
	j.keys = keys
	j.lastFetch = time.Now()
	j.mu.Unlock()
	return nil
}

// TransitVerifier verifies assertions and remembers every jti until it expires, so a
// captured assertion is refused the second time it is presented.
type TransitVerifier struct {
	keys   TransitKeys
	issuer string
	now    func() time.Time

	mu        sync.Mutex
	seen      map[string]int64 // jti -> the unix second after which it can be forgotten
	lastPrune int64
}

func NewTransitVerifier(keys TransitKeys, issuer string) *TransitVerifier {
	if issuer == "" {
		issuer = DefaultTransitIssuer
	}
	return &TransitVerifier{keys: keys, issuer: issuer, now: time.Now, seen: map[string]int64{}}
}

// Verify checks token for the request method + host + target (the request-target as sent,
// path and query) on behalf of userID (the x-user-id header). It accepts each valid
// assertion exactly once.
func (v *TransitVerifier) Verify(token, method, host, target, userID string) (TransitClaims, error) {
	var c TransitClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, ErrTransitMalformed
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if !decodeSegment(parts[0], &hdr) || hdr.Alg != "EdDSA" || hdr.Kid == "" {
		return c, ErrTransitMalformed
	}
	key, ok := v.keys.Key(hdr.Kid)
	if !ok {
		return c, ErrTransitUnknownKey
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return c, ErrTransitBadSignature
	}
	if !decodeSegment(parts[1], &c) || c.Iss == "" || c.Sub == "" || c.Jti == "" || c.Iat <= 0 || c.Exp <= 0 {
		return TransitClaims{}, ErrTransitMalformed
	}

	now := v.now().Unix()
	switch {
	case c.Exp < now-TransitLeewaySeconds:
		return TransitClaims{}, ErrTransitExpired
	case c.Iss != v.issuer:
		return TransitClaims{}, ErrTransitWrongIssuer
	case c.Exp-c.Iat > TransitMaxLifetimeSeconds:
		return TransitClaims{}, ErrTransitTooLong
	case c.Iat > now+TransitLeewaySeconds:
		return TransitClaims{}, ErrTransitMalformed
	case c.Htm != method || c.Htu != host+target:
		return TransitClaims{}, ErrTransitWrongRequest
	case c.Amr == "jwt" && c.Sub != userID:
		return TransitClaims{}, ErrTransitWrongUser
	}

	// Only a fully valid assertion is remembered, so garbage cannot fill the cache or burn
	// a real jti.
	v.mu.Lock()
	defer v.mu.Unlock()
	if now > v.lastPrune {
		for jti, until := range v.seen {
			if until < now {
				delete(v.seen, jti)
			}
		}
		v.lastPrune = now
	}
	if _, used := v.seen[c.Jti]; used {
		return TransitClaims{}, ErrTransitReplayed
	}
	v.seen[c.Jti] = c.Exp + TransitLeewaySeconds
	return c, nil
}

func decodeSegment(seg string, into any) bool {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	return err == nil && json.Unmarshal(b, into) == nil
}

// TransitMiddleware is Middleware for a gateway that signs transit assertions: a request
// reaches next only with an assertion the gateway minted for it, and the identity in context
// comes from the signed claims, not from headers. A cert-authorized (mtls) or agent request
// transits with no user identity, exactly as the gateway forwards it.
func TransitMiddleware(v *TransitVerifier, exempt func(path string) bool) func(http.Handler) http.Handler {
	if v == nil || v.keys == nil {
		panic("gatewayauth: refusing to start — transit assertion verifier has no keys")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if exempt != nil && exempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// The gateway signs :path exactly as it saw it, so compare the request-target as
			// received; only an absolute-form target is reduced to its path and query.
			target := r.RequestURI
			if !strings.HasPrefix(target, "/") {
				target = r.URL.RequestURI()
			}
			c, err := v.Verify(r.Header.Get(HeaderGatewayAuth), r.Method, r.Host, target, r.Header.Get(HeaderUserID))
			if err != nil {
				slog.Warn("gatewayauth: transit assertion refused", "reason", err.Error(), "method", r.Method, "path", r.URL.Path)
				unauthorized(w)
				return
			}
			id := Identity{LensWorkspace: r.Header.Get(HeaderLensWorkspace)}
			if c.Amr == "jwt" {
				id.Email = c.Email
				id.UserID = c.Sub
				id.Teams = strings.Join(c.Teams, ",")
				id.Issuer = c.Idp
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}
