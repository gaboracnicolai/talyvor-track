// Package config loads runtime configuration from environment vars.
// The Track server is intentionally minimal in this phase — Postgres
// URL is mandatory, everything else has a sensible default.
package config

import (
	"errors"
	"fmt"
	"os"
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
}

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
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("%w: TRACK_DATABASE_URL", ErrMissingEnv)
	}
	// Fail closed: the auth trust boundary depends on this secret. Unset or shorter
	// than the gateway's minimum → refuse to start rather than run insecure.
	if len(c.GatewayAuthSecret) < MinGatewayAuthSecretLen {
		return nil, fmt.Errorf("%w: GATEWAY_AUTH_SECRET must be set and >= %d chars (Track's copy of the edge gateway transit-proof secret)", ErrMissingEnv, MinGatewayAuthSecretLen)
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
