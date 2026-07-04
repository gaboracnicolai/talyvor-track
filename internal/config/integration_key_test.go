package config_test

import (
	"encoding/base64"
	"testing"

	"github.com/talyvor/track/internal/config"
)

func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TRACK_DATABASE_URL", "postgres://x")
	t.Setenv("GATEWAY_AUTH_SECRET", "0123456789abcdef0123") // >= MinGatewayAuthSecretLen
}

// (f) CONFIG VALIDATION: the integration encryption key is validated at BOOT (Load), not at first use — a
// wrong length or non-base64 value fails Load; absent leaves it disabled (nil) with Track still loading.
func TestConfig_IntegrationKey_Validation(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(make([]byte, config.IntegrationEncryptionKeyLen))
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))

	t.Run("valid 32-byte key loads", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("TRACK_INTEGRATION_ENCRYPTION_KEY", valid)
		c, err := config.Load()
		if err != nil {
			t.Fatalf("valid key should load: %v", err)
		}
		if len(c.IntegrationEncryptionKey) != config.IntegrationEncryptionKeyLen {
			t.Fatalf("key len = %d, want %d", len(c.IntegrationEncryptionKey), config.IntegrationEncryptionKeyLen)
		}
	})

	t.Run("wrong length fails at boot", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("TRACK_INTEGRATION_ENCRYPTION_KEY", short)
		if _, err := config.Load(); err == nil {
			t.Fatal("a 16-byte key must fail Load (boot), not silently pass to first use")
		}
	})

	t.Run("non-base64 fails at boot", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("TRACK_INTEGRATION_ENCRYPTION_KEY", "!!!not-base64!!!")
		if _, err := config.Load(); err == nil {
			t.Fatal("a non-base64 key must fail Load")
		}
	})

	t.Run("absent leaves it disabled (nil)", func(t *testing.T) {
		setBaseEnv(t)
		t.Setenv("TRACK_INTEGRATION_ENCRYPTION_KEY", "")
		c, err := config.Load()
		if err != nil {
			t.Fatalf("absent key must not fail Load: %v", err)
		}
		if c.IntegrationEncryptionKey != nil {
			t.Fatal("absent key should leave IntegrationEncryptionKey nil (store disabled)")
		}
	})
}
