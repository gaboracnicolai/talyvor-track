// Package config loads runtime configuration from environment vars.
// The Track server is intentionally minimal in this phase — Postgres
// URL is mandatory, everything else has a sensible default.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddr  string
	DatabaseURL string
	LogLevel    string

	// Talyvor Lens integration. All three are optional — an empty
	// LensURL keeps Track running in standalone mode (no AI cost
	// data, but every other endpoint works).
	LensURL           string
	LensAPIKey        string
	LensWebhookSecret string

	// LensWebhookFreshness (SEC-7) is the max age of a Lens spend alert's signed
	// emitted_at; an older alert is rejected by the webhook (a replay guard for
	// captures re-POSTed after the dedup key was pruned). TRACK_LENS_WEBHOOK_FRESHNESS
	// as a Go duration; default 5m. <=0 disables the check.
	LensWebhookFreshness time.Duration

	// GatewayAuthSecret is Track's copy of the edge gateway's transit-proof secret
	// (edge-infra GATEWAY_AUTH_SECRET). The auth middleware constant-time-compares the
	// inbound x-gateway-auth header against it to prove a request transited the gateway
	// before any gateway-injected identity header is trusted. REQUIRED, fail-closed:
	// Track refuses to start without it — starting without it would mean trusting
	// spoofable identity headers.
	GatewayAuthSecret string

	// High-availability realtime fan-out (T13). HAEnabled (TRACK_HA_ENABLED) is
	// strictly opt-in and OFF by default — when off, Track runs as a single
	// instance exactly as before and never touches Redis. When on, the realtime
	// hub mirrors events across instances over Redis pub/sub at RedisURL
	// (TRACK_REDIS_URL). Both are optional: HA off means RedisURL is ignored.
	HAEnabled bool
	RedisURL  string

	// IntegrationEncryptionKey is the 32-byte AES-256-GCM key that encrypts per-workspace provider API tokens
	// (Build C, workspace_integrations) before they touch the DB. Supplied base64-encoded via
	// TRACK_INTEGRATION_ENCRYPTION_KEY (`openssl rand -base64 32`). OPTIONAL: unset ⇒ the integration store is
	// disabled and live API import is unavailable (Track still runs). SET ⇒ validated to exactly 32 decoded
	// bytes at boot — a wrong length is fail-LOUD at startup, never a broken-crypto surprise at first use.
	IntegrationEncryptionKey []byte

	// MemberSyncSecret is the bearer token the service-authenticated members endpoint
	// (GET /v1/service/members) constant-time-compares. OPTIONAL: unset ⇒ Track boots
	// but that endpoint 401s all requests (member-sync disabled). If SET it must be
	// >= MinMemberSyncSecretLen — a weak secret would leak every tenant's roster, so a
	// misconfigured one fails LOUD at boot, not silently at first use.
	//
	// OPERATIONAL CONTRACT (not just code posture — ops MUST honor this):
	//   - This one token gates ALL-TENANT member data: a valid holder can read EVERY
	//     workspace's roster via /v1/service/members. Treat it as top-tier.
	//   - It MUST be a DEDICATED secret — never reused for any other purpose or service.
	//   - It MUST live only in the member-sync consumer's server-side environment
	//     (the Docs sync). NEVER client-side, NEVER in any browser-reachable config.
	//   - Rotation is expected. Rotating it requires updating BOTH sides in lockstep:
	//     this env on Track AND the Docs sync consumer (PR-2). A strong secret copied
	//     into three configs is no longer strong.
	//   - Every pull is audit-logged (event=service_member_pull, workspace_id + count);
	//     a leaked-token mass-enumeration is detectable there.
	MemberSyncSecret string
}

// MinMemberSyncSecretLen mirrors GATEWAY_AUTH_SECRET's minimum — this token gates the
// highest-value cross-tenant data (every workspace's member roster).
const MinMemberSyncSecretLen = 16

// IntegrationEncryptionKeyLen is the required decoded key length: 32 bytes for AES-256.
const IntegrationEncryptionKeyLen = 32

// MinGatewayAuthSecretLen mirrors the edge gateway's GATEWAY_AUTH_SECRET minimum
// (edge-infra auth-service config.rs); a shorter shared secret on either side is a
// configuration error.
const MinGatewayAuthSecretLen = 16

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:           getEnv("TRACK_LISTEN_ADDR", "0.0.0.0:3000"),
		DatabaseURL:          os.Getenv("TRACK_DATABASE_URL"),
		LogLevel:             getEnv("TRACK_LOG_LEVEL", "info"),
		LensURL:              os.Getenv("TRACK_LENS_URL"),
		LensAPIKey:           os.Getenv("TRACK_LENS_API_KEY"),
		LensWebhookSecret:    os.Getenv("TRACK_LENS_WEBHOOK_SECRET"),
		GatewayAuthSecret:    os.Getenv("GATEWAY_AUTH_SECRET"),
		HAEnabled:            parseBool(os.Getenv("TRACK_HA_ENABLED")),
		RedisURL:             os.Getenv("TRACK_REDIS_URL"),
		LensWebhookFreshness: getEnvDuration("TRACK_LENS_WEBHOOK_FRESHNESS", 5*time.Minute),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("%w: TRACK_DATABASE_URL", ErrMissingEnv)
	}
	// Fail closed: the auth trust boundary depends on this secret. Unset or shorter
	// than the gateway's minimum → refuse to start rather than run insecure.
	if len(c.GatewayAuthSecret) < MinGatewayAuthSecretLen {
		return nil, fmt.Errorf("%w: GATEWAY_AUTH_SECRET must be set and >= %d chars (Track's copy of the edge gateway transit-proof secret)", ErrMissingEnv, MinGatewayAuthSecretLen)
	}
	// Integration token-encryption key — OPTIONAL, but if provided it must decode to exactly 32 bytes.
	// Fail-LOUD at boot on a misconfigured key (wrong length / not base64), never a broken-crypto surprise at
	// first use. Absent ⇒ IntegrationEncryptionKey stays nil ⇒ the integration store is disabled.
	if v := os.Getenv("TRACK_INTEGRATION_ENCRYPTION_KEY"); v != "" {
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("%w: TRACK_INTEGRATION_ENCRYPTION_KEY must be valid base64: %v", ErrMissingEnv, err)
		}
		if len(key) != IntegrationEncryptionKeyLen {
			return nil, fmt.Errorf("%w: TRACK_INTEGRATION_ENCRYPTION_KEY must decode to exactly %d bytes (AES-256), got %d", ErrMissingEnv, IntegrationEncryptionKeyLen, len(key))
		}
		c.IntegrationEncryptionKey = key
	}
	// Member-sync bearer secret — OPTIONAL, but if set must be strong (it gates every
	// tenant's roster). Boot-fail-closed on a weak value; unset leaves the endpoint 401.
	c.MemberSyncSecret = os.Getenv("TRACK_MEMBER_SYNC_SECRET")
	if c.MemberSyncSecret != "" && len(c.MemberSyncSecret) < MinMemberSyncSecretLen {
		return nil, fmt.Errorf("%w: TRACK_MEMBER_SYNC_SECRET, if set, must be >= %d chars (it gates every tenant's member roster)", ErrMissingEnv, MinMemberSyncSecretLen)
	}
	return c, nil
}

var ErrMissingEnv = errors.New("missing required environment variable")

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getEnvDuration parses a Go duration env var, falling back on unset OR invalid
// (a bad value must not silently disable a security window — it takes the default).
func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// parseBool treats "1"/"true"/"yes"/"on" (any case) as true; everything else,
// including empty, is false. Keeps opt-in flags off-by-default and forgiving.
func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
