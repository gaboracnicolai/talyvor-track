package config_test

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/talyvor/track/internal/config"
)

// shipped_defaults_test.go — WHAT THIS PROCESS IS CONFIGURED AS WHEN THE OPERATOR SETS NOTHING.
//
// ⚠ MEASURED BEFORE IT WAS WRITTEN, BY MUTATION RATHER THAN BY READING (W3.49, tab-k4m7,
// ~/talyvor-queue/w349-default-census-k4m7.py). The census changed one shipped default at a
// time and ran the whole suite against real Postgres: RED means something depends on the
// value, GREEN means it can be set to anything at all. Of 61 defaults in this repo, 48 were
// GREEN. Three of them are in this file's subject and they are the three that decide posture:
//
//	TRACK_LISTEN_ADDR fallback   0.0.0.0:3000 -> 127.0.0.1:3999  ENTIRE SUITE STAYED GREEN
//	TRACK_LENS_WEBHOOK_FRESHNESS 5m -> 47m                        ENTIRE SUITE STAYED GREEN
//	parseBool's default          false -> true                    ENTIRE SUITE STAYED GREEN
//
// The middle one is SEC-7's replay window: how old a signed Lens spend alert may be and still
// be accepted. The third is the off-by-default of every opt-in boolean — flip it and an
// operator who set nothing is running TRACK_HA_ENABLED, talking to Redis. Neither had a test
// whose outcome depended on the value.
//
// ⚠ WHY THIS IS AN OBSERVATION AND NOT A SOURCE ASSERTION, WHICH IS THE WHOLE DESIGN. The
// bind default is the second argument of a getEnv() call — it is not a named constant and it
// is not an `if x == "" ` fallback, so NEITHER shape a source-walking census looks for can
// see it. cmd/track/shipped_defaults_test.go says so about itself. The only instrument that
// sees this value is the one that clears the environment, calls the real Load(), and reads
// the struct that comes back. Inferring a default from source is how a default gets missed.
//
// ⚠ WHAT THIS FILE DOES NOT PROVE, stated so nobody over-reads it: that any of these is the
// RIGHT value, and that anything ENFORCES it. Pinning ListenAddr does not prove the server
// binds there — main.go's use of it is a separate question. Whether 0.0.0.0 (every
// interface) is the right bind for this service is an operations decision and is not taken
// here. What changes is that altering one of these becomes an edit to a named table with a
// reason, instead of a silent one-token diff no test can see.

// clearConfigEnv removes every variable Load() can read, so the struct that comes back is a
// statement about the DEFAULTS and not about the shell this test happens to run in. It clears
// by PREFIX from the live environment rather than from a hardcoded list: a list would go stale
// the moment a variable is added, and would go stale SILENTLY — the failure mode this whole
// file exists to close. t.Setenv restores everything at test end.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "TRACK_") || strings.HasPrefix(k, "GATEWAY_") {
			t.Setenv(k, "")
		}
	}
}

// theTwoRequired are the only variables Load() refuses to start without. They are set to
// syntactically valid throwaway values so the rest of the struct is observable at all.
func theTwoRequired(t *testing.T) {
	t.Helper()
	t.Setenv("TRACK_DATABASE_URL", "postgres://user:pw@localhost:5432/x?sslmode=disable")
	t.Setenv("GATEWAY_AUTH_SECRET", "0123456789abcdef0123456789abcdef")
}

func TestShippedDefaults_AnEmptyEnvironmentProducesExactlyThis(t *testing.T) {
	clearConfigEnv(t)
	theTwoRequired(t)

	c, err := config.Load()
	if err != nil {
		t.Fatalf("Load() with only the two required vars set: %v", err)
	}

	// Every row is "what an operator who configured nothing is running". A change here is a
	// change to shipped posture and must be argued for in the same commit.
	checks := []struct {
		field string
		got   any
		want  any
		note  string
	}{
		{"ListenAddr", c.ListenAddr, "0.0.0.0:3000",
			"BIND ADDRESS. 0.0.0.0 is every interface, not loopback — this is the posture, not a detail"},
		{"LensWebhookFreshness", c.LensWebhookFreshness, 5 * time.Minute,
			"SEC-7 REPLAY WINDOW: the max age of a signed Lens alert. Widening it widens the replay window"},
		{"HAEnabled", c.HAEnabled, false,
			"OPT-IN, OFF. parseBool returns false for absent/unrecognised, so HA never starts by accident"},

		// The credentials and endpoints are all EMPTY by default, and each empty means a
		// specific thing is off. An accidental non-empty default here would be a shipped
		// credential — the exact defect config.publishedGatewaySecrets exists to punish.
		{"DatabaseURL", c.DatabaseURL, "postgres://user:pw@localhost:5432/x?sslmode=disable", "required; echoes what was set"},
		{"GatewayAuthSecret", c.GatewayAuthSecret, "0123456789abcdef0123456789abcdef", "required; echoes what was set"},
		{"LensURL", c.LensURL, "", "empty ⇒ standalone mode, no AI cost data"},
		{"LensAPIKey", c.LensAPIKey, "", "empty ⇒ no Lens credential"},
		{"LensMintKey", c.LensMintKey, "", "empty ⇒ no per-workspace token minting"},
		{"LensWebhookSecret", c.LensWebhookSecret, "", "empty ⇒ unsigned alerts are not accepted"},
		{"LensDashboardURL", c.LensDashboardURL, "", "empty ⇒ no lens_url link is emitted at all"},
		{"RedisURL", c.RedisURL, "", "empty ⇒ ignored, because HAEnabled is false"},
		{"MemberSyncSecret", c.MemberSyncSecret, "", "empty ⇒ /v1/service/members 401s everything"},
	}

	seen := map[string]bool{}
	for _, ch := range checks {
		seen[ch.field] = true
		if !reflect.DeepEqual(ch.got, ch.want) {
			t.Errorf("config.%s (%s):\n  process runs at %#v\n  recorded default %#v\n"+
				"If this change is deliberate, edit the recorded value in this file in the SAME "+
				"commit and say why. That is the entire point of this table.", ch.field, ch.note, ch.got, ch.want)
		}
	}

	// IntegrationEncryptionKey is []byte; nil is its "absent" and DeepEqual against a typed
	// nil is fiddly, so it is asserted directly rather than through the table.
	seen["IntegrationEncryptionKey"] = true
	if c.IntegrationEncryptionKey != nil {
		t.Errorf("config.IntegrationEncryptionKey: %d bytes with no env var set — the integration "+
			"store must be DISABLED by default, never enabled with a key from nowhere", len(c.IntegrationEncryptionKey))
	}

	// COMPLETENESS FLOOR, and it is the half that stops this guard narrowing. Every assertion
	// above is a row in a hand-written list, and a hand-written list of a struct's fields is
	// stale the moment a field is added — stale in the direction of looking fine. Deriving the
	// population from the type means a new Config field must be observed here or this reds.
	typ := reflect.TypeOf(*c)
	if typ.NumField() < 13 {
		t.Fatalf("Config has %d fields; this guard was written against 13. A field count that "+
			"FELL means fields were removed and the rows above may be pinning nothing", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		if name := typ.Field(i).Name; !seen[name] {
			t.Errorf("config.%s is a Config field with NO recorded default. Add a row above "+
				"stating what an operator who sets nothing gets — a field nobody observed is "+
				"exactly how the three findings in this file's header escaped for months", name)
		}
	}
}
