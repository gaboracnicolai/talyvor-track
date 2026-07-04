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
}

// IntegrationEncryptionKeyLen is the required decoded key length: 32 bytes for AES-256.
const IntegrationEncryptionKeyLen = 32

// MinGatewayAuthSecretLen mirrors the edge gateway's GATEWAY_AUTH_SECRET minimum
// (edge-infra auth-service config.rs); a shorter shared secret on either side is a
// configuration error.
const MinGatewayAuthSecretLen = 16

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:        getEnv("TRACK_LISTEN_ADDR", "0.0.0.0:3000"),
		DatabaseURL:       os.Getenv("TRACK_DATABASE_URL"),
		LogLevel:          getEnv("TRACK_LOG_LEVEL", "info"),
		LensURL:           os.Getenv("TRACK_LENS_URL"),
		LensAPIKey:        os.Getenv("TRACK_LENS_API_KEY"),
		LensWebhookSecret: os.Getenv("TRACK_LENS_WEBHOOK_SECRET"),
		GatewayAuthSecret: os.Getenv("GATEWAY_AUTH_SECRET"),
		HAEnabled:         parseBool(os.Getenv("TRACK_HA_ENABLED")),
		RedisURL:          os.Getenv("TRACK_REDIS_URL"),
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
	return c, nil
}

var ErrMissingEnv = errors.New("missing required environment variable")

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
