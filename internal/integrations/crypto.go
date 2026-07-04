// Package integrations is the per-workspace provider-credential store for the live importer (Build C). It
// holds LIVE customer API tokens (Linear/Jira), encrypted at rest with AES-256-GCM — plaintext exists ONLY
// in memory, at use time, and is never persisted, logged, or returned by any endpoint.
package integrations

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// Cipher encrypts/decrypts provider tokens with AES-256-GCM using a 32-byte key from config
// (TRACK_INTEGRATION_ENCRYPTION_KEY). A FRESH random nonce per encryption ⇒ two encryptions of the same
// plaintext yield different ciphertext (no deterministic-encryption leak). GCM authenticates the ciphertext,
// so a tampered ciphertext/nonce fails Decrypt cleanly (an error) rather than returning garbage or panicking.
type Cipher struct{ aead cipher.AEAD }

// NewCipher builds the AEAD from a 32-byte key. Wrong length ⇒ error (the config layer already fails boot;
// this is the last guard so a bad key can't silently produce a broken Cipher).
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("integrations: key must be 32 bytes for AES-256, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns (ciphertext, nonce) for plaintext with a fresh random nonce (crypto/rand).
func (c *Cipher) Encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return c.aead.Seal(nil, nonce, plaintext, nil), nonce, nil
}

// Decrypt returns the plaintext for (ciphertext, nonce). A tampered/corrupt input fails the GCM auth tag ⇒
// returns an error, never a garbage plaintext, never a panic.
func (c *Cipher) Decrypt(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != c.aead.NonceSize() {
		return nil, errors.New("integrations: bad nonce length")
	}
	pt, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("integrations: decrypt (auth) failed: %w", err)
	}
	return pt, nil
}
