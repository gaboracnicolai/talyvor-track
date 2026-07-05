package config_test

import (
	"testing"

	"github.com/talyvor/track/internal/config"
)

// TRACK_MEMBER_SYNC_SECRET protects the highest-value data (every tenant's roster),
// so a misconfigured secret must fail LOUD at boot, not silently 401. Posture:
// OPTIONAL (Track boots without member-sync), but if SET it must be >= 16 chars —
// mirrors GATEWAY_AUTH_SECRET's length gate.
func TestConfig_MemberSyncSecret_BootPosture(t *testing.T) {
	base := func() {
		t.Setenv("TRACK_DATABASE_URL", "postgres://x")
		t.Setenv("GATEWAY_AUTH_SECRET", "a-valid-32-char-shared-secret-ok!")
	}

	// set-but-short → Load MUST fail (boot-fail-closed on a weak secret).
	base()
	t.Setenv("TRACK_MEMBER_SYNC_SECRET", "tooshort")
	if _, err := config.Load(); err == nil {
		t.Fatal("short TRACK_MEMBER_SYNC_SECRET must fail Load (boot-fail-closed), got nil error")
	}

	// unset → Load OK (member-sync is optional; Track still boots).
	base()
	t.Setenv("TRACK_MEMBER_SYNC_SECRET", "")
	if _, err := config.Load(); err != nil {
		t.Fatalf("unset TRACK_MEMBER_SYNC_SECRET must NOT fail Load (optional): %v", err)
	}

	// valid (>=16) → Load OK.
	base()
	t.Setenv("TRACK_MEMBER_SYNC_SECRET", "a-strong-member-sync-secret-1234")
	if _, err := config.Load(); err != nil {
		t.Fatalf("valid TRACK_MEMBER_SYNC_SECRET must load: %v", err)
	}
}
