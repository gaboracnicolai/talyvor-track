package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/talyvor/track/internal/config"
	"github.com/talyvor/track/internal/gatewayauth"
)

// publishedSecret is the value docker-compose.yaml shipped as the GATEWAY_AUTH_SECRET
// default, from 6f1acc8 (#22) through a3bc7b2 — 42 of the 78 commits on main, in a PUBLIC
// repository.
//
// GATEWAY_AUTH_SECRET is the entire root of trust: knowing it means setting x-gateway-auth
// plus x-user-email and being any user in any workspace. It is 36 characters, so it
// SATISFIED the >= 16 length check — which is exactly why the fail-closed boot path never
// fired on the one configuration that needed it.
const publishedSecret = "dev-gateway-transit-secret-change-me"

// THE COMPOSE FIX STOPS THE DEFAULT; IT DOES NOT STOP THE VALUE.
//
// Removing the literal from HEAD does not un-publish it. Git history is permanent: anyone
// who has ever cloned, forked, or read the repo still has it, as does GitHub's code-search
// index. So the fix is not "stop shipping it" — it is "refuse it forever, regardless of
// length". A secret's strength is irrelevant once it is public.
//
// RED before the blocklist: Load() ACCEPTS publishedSecret because len(36) >= 16.
func TestLoad_RejectsPublishedSecret(t *testing.T) {
	if len(publishedSecret) < gatewayauth.MinSecretLen {
		t.Fatalf("premise wrong: the published placeholder is %d chars, under the %d minimum — "+
			"the length check would already have caught it and this guard would be redundant",
			len(publishedSecret), gatewayauth.MinSecretLen)
	}

	t.Setenv("TRACK_DATABASE_URL", "postgres://x")
	t.Setenv("GATEWAY_AUTH_SECRET", publishedSecret)

	_, err := config.Load()
	if err == nil {
		t.Fatalf("Load() ACCEPTED the published placeholder (%d chars, >= the %d minimum). "+
			"A value that has been readable in a public repo for 42 commits cannot be a secret, "+
			"however long it is — it must be rejected by identity, not by length.",
			len(publishedSecret), gatewayauth.MinSecretLen)
	}
	if !errors.Is(err, config.ErrMissingEnv) {
		t.Errorf("error = %v, want it to wrap ErrMissingEnv so callers classify it as a config failure", err)
	}
	// The message has to say WHY, or the operator's first instinct is to lengthen the value.
	for _, want := range []string{"PUBLISHED", "git history", "openssl rand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing %q — it must explain that the value is permanently\n"+
				"compromised and show how to generate a replacement.\ngot: %s", want, err.Error())
		}
	}
}

// A fresh operator-generated secret of the same length must still boot: the guard rejects
// specific published VALUES, not a length band or a shape. Without this, "reject everything"
// would pass the test above.
func TestLoad_AcceptsFreshSecretOfTheSameLength(t *testing.T) {
	fresh := "9f3a2b1c8d7e6f5a4b3c2d1e0f9a8b7c6d5e" // 36 chars, same as the published one
	if len(fresh) != len(publishedSecret) {
		t.Fatalf("fixture drift: fresh=%d published=%d — the lengths must match for this to "+
			"prove the guard is not a length rule", len(fresh), len(publishedSecret))
	}
	t.Setenv("TRACK_DATABASE_URL", "postgres://x")
	t.Setenv("GATEWAY_AUTH_SECRET", fresh)

	if _, err := config.Load(); err != nil {
		t.Fatalf("Load() refused a fresh secret of identical length: %v — the blocklist must "+
			"reject published values, not a length band", err)
	}
}

// A short secret must still report the LENGTH problem, not the blocklist one: the operator's
// next action differs, so the ordering of the two checks is load-bearing.
func TestLoad_ShortSecret_StillReportsLength(t *testing.T) {
	t.Setenv("TRACK_DATABASE_URL", "postgres://x")
	t.Setenv("GATEWAY_AUTH_SECRET", "tooshort")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() accepted an 8-char secret")
	}
	if !strings.Contains(err.Error(), "16") {
		t.Errorf("a short secret must report the length bound, not the blocklist; got: %s", err.Error())
	}
}
