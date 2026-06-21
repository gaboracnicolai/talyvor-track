package config_test

import (
	"errors"
	"testing"

	"github.com/talyvor/track/internal/config"
)

// TestLoad_GatewayAuthSecret_FailsClosed — the auth trust boundary depends on the
// transit-proof secret, so Track must REFUSE TO START (a clean config error, not a panic
// deep in a handler) when GATEWAY_AUTH_SECRET is unset or shorter than the gateway's
// minimum.
func TestLoad_GatewayAuthSecret_FailsClosed(t *testing.T) {
	t.Setenv("TRACK_DATABASE_URL", "postgres://x") // satisfy the other required var

	t.Setenv("GATEWAY_AUTH_SECRET", "") // unset / empty
	if _, err := config.Load(); err == nil || !errors.Is(err, config.ErrMissingEnv) {
		t.Errorf("empty GATEWAY_AUTH_SECRET: err=%v, want ErrMissingEnv (fail-closed)", err)
	}

	t.Setenv("GATEWAY_AUTH_SECRET", "tooshort") // < 16 chars
	if _, err := config.Load(); err == nil || !errors.Is(err, config.ErrMissingEnv) {
		t.Errorf("short GATEWAY_AUTH_SECRET: err=%v, want ErrMissingEnv", err)
	}

	t.Setenv("GATEWAY_AUTH_SECRET", "a-valid-32-char-shared-secret-ok!") // >= 16
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("valid GATEWAY_AUTH_SECRET should load: %v", err)
	}
	if cfg.GatewayAuthSecret == "" {
		t.Error("loaded config should carry the gateway auth secret")
	}
}
