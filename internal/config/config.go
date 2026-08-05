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

	"github.com/talyvor/track/internal/gatewayauth"
)

type Config struct {
	ListenAddr  string
	DatabaseURL string
	LogLevel    string

	// Talyvor Lens integration. All three are optional — an empty
	// LensURL keeps Track running in standalone mode (no AI cost
	// data, but every other endpoint works).
	LensURL    string
	LensAPIKey string
	// LensMintKey is Lens's LENS_MINT_KEY: a narrow credential whose ONLY power is minting a
	// per-workspace token. It is separate from LensAPIKey because the two roles need different
	// credentials — see internal/ai.New. Never set this to Lens's global LENS_API_KEY.
	LensMintKey       string
	LensWebhookSecret string

	// LensDashboardURL is where the AI-cost responses link a human to see request-level
	// spend — emitted as `lens_url`. OPTIONAL and empty by default; unset means no link is
	// emitted at all.
	//
	// IT IS CONFIGURED RATHER THAN DERIVED, and that is the whole point. Track used to build
	// this link as LensURL + "/dashboard" whenever LensURL was set. But LensURL is Lens's API
	// base, and /dashboard is a BROWSER route that Lens registers only under
	// LENS_DASHBOARD_ENABLED (default false) — a variable Lens's own docker-compose does not
	// even forward, so the path is unreachable on the standard deploy whatever the operator
	// sets. Track cannot observe another service's feature flag, and mirroring it here would
	// be two values that must agree with nothing comparing them. So the destination is
	// declared, or there is no link. TRACK_LENS_DASHBOARD_URL.
	LensDashboardURL string

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

// publishedGatewaySecrets are GATEWAY_AUTH_SECRET values that have been PUBLISHED — shipped
// as a default in this repo's compose file, and therefore committed to git history.
//
// Git history is permanent: deleting a value from HEAD does not un-publish it. Anyone who
// has ever cloned, forked, or read this repository has it — as does GitHub's code-search
// index — and with it can set x-gateway-auth + x-user-email and be any user in any
// workspace. So these are rejected FOREVER, regardless of length: a secret's strength is
// irrelevant once it is public.
//
// This is what makes the boot check a real guard rather than a formality. The shipped
// default was 36 characters and SATISFIED the >= 16 bound, so the fail-closed path never
// fired on the one configuration that actually needed it. The compose fix stops the
// DEFAULT; this stops the VALUE — including someone pasting it out of git history.
//
// Add to this list, never remove from it. Ported from talyvor-docs, which hit the identical
// defect independently.
var publishedGatewaySecrets = map[string]bool{
	// docker-compose.yaml, from 6f1acc8 (#22) through a3bc7b2 — 42 of 78 commits on main.
	"dev-gateway-transit-secret-change-me": true,
}

// MinGatewayAuthSecretLen is re-exported from internal/gatewayauth, which OWNS the rule
// (the boundary that enforces it also defines the minimum it can defend). Two separately
// declared numbers could drift; this cannot. gatewayauth.Middleware refuses to start
// below it regardless, so this boot check is the friendly early failure, not the guard.
const MinGatewayAuthSecretLen = gatewayauth.MinSecretLen

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:           getEnv("TRACK_LISTEN_ADDR", "0.0.0.0:3000"),
		DatabaseURL:          os.Getenv("TRACK_DATABASE_URL"),
		LogLevel:             getEnv("TRACK_LOG_LEVEL", "info"),
		LensURL:              os.Getenv("TRACK_LENS_URL"),
		LensAPIKey:           os.Getenv("TRACK_LENS_API_KEY"),
		LensMintKey:          os.Getenv("TRACK_LENS_MINT_KEY"),
		LensWebhookSecret:    os.Getenv("TRACK_LENS_WEBHOOK_SECRET"),
		LensDashboardURL:     strings.TrimSpace(os.Getenv("TRACK_LENS_DASHBOARD_URL")),
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
	// …and not a value that has already been published. Checked AFTER the length bound so a
	// short secret still gets the more useful "too short" message, and deliberately not
	// constant-time: these values are public by definition, so there is nothing to leak.
	if publishedGatewaySecrets[c.GatewayAuthSecret] {
		return nil, fmt.Errorf("%w: GATEWAY_AUTH_SECRET is a PUBLISHED placeholder from this "+
			"repo and is permanently compromised — it is in git history, so it cannot be made "+
			"secret again. Generate a fresh value: openssl rand -hex 32", ErrMissingEnv)
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
