package config_test

import (
	"testing"

	"github.com/talyvor/track/internal/config"
	"github.com/talyvor/track/internal/integrations"
)

// TestIntegrationKeyLen_IsTheLengthTheCipherActuallyAccepts couples the two 32s that
// have to agree, by MEASURING one of them instead of re-declaring it.
//
// ⚠ WHY THIS EXISTS. The AES-256 key length is written twice in product source —
// config.IntegrationEncryptionKeyLen (the boot check) and a hardcoded `len(key) != 32`
// inside integrations.NewCipher (the last guard) — and until this test the only
// statement anywhere that the two are the same number was a COMMENT in
// cmd/track/main.go: `// unreachable (config validated the length)`.
//
// ⚠⚠ THAT COMMENT IS FALSE FOR ANY DRIFT, MEASURED BY RUNNING THE REAL BINARY
// (W3.43, ~/talyvor-queue/w343-keylen-probe-j4q7.py, against real Postgres). With the
// config constant moved to 16, 24 or 235 and a matching key supplied, Track exits 1
// from that supposedly-unreachable branch with
//
//	integrations: cipher init failed — key must be 32 bytes for AES-256, got 16
//
// — an error from the integrations package that never names
// TRACK_INTEGRATION_ENCRYPTION_KEY, which is the variable the operator actually set.
// The shipped arms are correct: a 16- or 24-byte key against the real constant is
// refused AT CONFIG, naming the variable.
//
// ⚠⚠⚠ AND THE GOOD NEWS IS PART OF THE FINDING, STATED RATHER THAN OMITTED: the
// crypto is NEVER silently weakened. 16 and 24 are legal AES key lengths that
// crypto/aes would accept, so a drifted constant could in principle have handed
// AES-128 to a path whose every comment says AES-256 — it does not, because
// NewCipher's own literal refuses first. What drift costs is a boot failure that
// misnames its cause, not confidentiality. The census row that started this
// (W3.42: the constant is unpinned in BOTH directions) is real; this is how far it
// actually reaches.
//
// W3.42 merged a fix for a test that compared a value against the constant that
// produced it. A guard here that simply re-declared 32 would be a third copy of the
// number and would pass with both real copies moved together — the same defect in a
// new costume. So the two assertions below are deliberately independent.
func TestIntegrationKeyLen_IsTheLengthTheCipherActuallyAccepts(t *testing.T) {
	// (1) COUPLING — measured, not declared. Ask NewCipher which key lengths it
	// accepts by calling it, and require that set to be exactly the one length the
	// boot check enforces. Reds if EITHER number moves on its own.
	var accepted []int
	for n := 0; n <= 64; n++ {
		if _, err := integrations.NewCipher(make([]byte, n)); err == nil {
			accepted = append(accepted, n)
		}
	}
	if len(accepted) != 1 {
		t.Fatalf("integrations.NewCipher accepts %v key lengths, want exactly one — "+
			"the boot check enforces a single length and cannot represent a set", accepted)
	}
	if accepted[0] != config.IntegrationEncryptionKeyLen {
		t.Errorf("NewCipher accepts a %d-byte key but config.IntegrationEncryptionKeyLen is %d — "+
			"a key that passes the boot check would be refused by the cipher (or vice versa), and "+
			"cmd/track/main.go calls that branch unreachable",
			accepted[0], config.IntegrationEncryptionKeyLen)
	}

	// (2) THE STANDARD — the assertion that survives BOTH numbers moving together.
	// 32 here is not a third copy of an internal choice: it is the key size of
	// AES-256, an external fact, and AES-256 is what four separate comments on this
	// path promise. crypto/aes also accepts 16 and 24 (AES-128/192), so nothing in
	// the stdlib would object to a matched pair at either of those.
	const aes256KeyBytes = 32
	if config.IntegrationEncryptionKeyLen != aes256KeyBytes {
		t.Errorf("IntegrationEncryptionKeyLen = %d, want %d — the workspace-integration token store "+
			"documents itself as AES-256-GCM, and 16 or 24 would be AES-128/192 accepted silently by crypto/aes",
			config.IntegrationEncryptionKeyLen, aes256KeyBytes)
	}
}
