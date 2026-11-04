// Package crypto holds small helpers for secrets at rest and random tokens.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// Box encrypts small strings with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

// NewBox derives a 256-bit key from secret.
func NewBox(secret string) (*Box, error) {
	sum := sha256.Sum256([]byte("mcp-gateway/v1:" + secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plain. The empty string stays empty.
func (b *Box) Seal(plain string) string {
	if plain == "" {
		return ""
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return base64.RawStdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), nil))
}

// Open reverses Seal.
func (b *Box) Open(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("ciphertext too short")
	}
	out, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// RandToken returns n random bytes, base64url encoded.
func RandToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// RandHex returns n random bytes, hex encoded.
func RandHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

// Hash returns the hex SHA-256 of s. Used to store bearer tokens, which are
// high-entropy, so a fast hash is appropriate.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
