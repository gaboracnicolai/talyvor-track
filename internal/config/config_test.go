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

// TRACK_LENS_DASHBOARD_URL is optional and defaults to empty — an unset destination must
// stay unset, because empty is what makes the handler omit `lens_url` entirely rather than
// linking somewhere it has not been told about.
func TestLoad_LensDashboardURL(t *testing.T) {
	t.Setenv("TRACK_DATABASE_URL", "postgres://x/y")
	t.Setenv("GATEWAY_AUTH_SECRET", "0123456789abcdef0123456789abcdef")

	t.Setenv("TRACK_LENS_DASHBOARD_URL", "")
	c, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LensDashboardURL != "" {
		t.Errorf("unset dashboard URL = %q, want empty", c.LensDashboardURL)
	}

	// Whitespace is not configuration — it would emit a link to nothing.
	t.Setenv("TRACK_LENS_DASHBOARD_URL", "   ")
	if c, err = config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LensDashboardURL != "" {
		t.Errorf("whitespace dashboard URL = %q, want empty", c.LensDashboardURL)
	}

	t.Setenv("TRACK_LENS_DASHBOARD_URL", " https://app.talyvor.com/lens ")
	if c, err = config.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.LensDashboardURL != "https://app.talyvor.com/lens" {
		t.Errorf("dashboard URL = %q, want the trimmed value", c.LensDashboardURL)
	}
}
