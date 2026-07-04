package integrations

import (
	"bytes"
	"testing"
)

// White-box crypto proofs (b nonce uniqueness, e tamper) — pure, no PG.

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// (b) NONCE UNIQUENESS: two encryptions of the SAME plaintext produce DIFFERENT ciphertext + nonce (fresh
// random nonce each time) — no deterministic-encryption leak. Both still decrypt to the original.
func TestCipher_FreshNoncePerEncryption(t *testing.T) {
	c := testCipher(t)
	ct1, n1, err := c.Encrypt([]byte("secret-xyz"))
	if err != nil {
		t.Fatal(err)
	}
	ct2, n2, err := c.Encrypt([]byte("secret-xyz"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(n1, n2) {
		t.Fatal("nonce reused across encryptions")
	}
	if bytes.Equal(ct1, ct2) {
		t.Fatal("same ciphertext for same plaintext — deterministic-encryption leak")
	}
	for _, tc := range []struct{ ct, n []byte }{{ct1, n1}, {ct2, n2}} {
		pt, err := c.Decrypt(tc.ct, tc.n)
		if err != nil || string(pt) != "secret-xyz" {
			t.Fatalf("decrypt = %q, %v; want secret-xyz", pt, err)
		}
	}
}

// (e) TAMPER: a flipped ciphertext byte, a wrong nonce, or a truncated nonce fails the GCM auth tag — an
// error, never a garbage plaintext, never a panic.
func TestCipher_TamperFailsCleanly(t *testing.T) {
	c := testCipher(t)
	ct, n, _ := c.Encrypt([]byte("secret-xyz"))

	bad := bytes.Clone(ct)
	bad[0] ^= 0xff
	if _, err := c.Decrypt(bad, n); err == nil {
		t.Fatal("tampered ciphertext must fail Decrypt")
	}
	_, n2, _ := c.Encrypt([]byte("other"))
	if _, err := c.Decrypt(ct, n2); err == nil {
		t.Fatal("wrong nonce must fail Decrypt")
	}
	if _, err := c.Decrypt(ct, n[:4]); err == nil {
		t.Fatal("bad nonce length must fail Decrypt")
	}
}

func TestNewCipher_RejectsBadKeyLen(t *testing.T) {
	if _, err := NewCipher([]byte("short")); err == nil {
		t.Fatal("must reject a non-32-byte key")
	}
}
