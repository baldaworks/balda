// Package secrets protects application-owned values at rest.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// ErrUnavailable reports missing, invalid, or unusable protection material.
// It intentionally does not distinguish key and ciphertext failures.
var ErrUnavailable = errors.New("secret protection unavailable")

const (
	formatVersion    = byte(1)
	maxPlaintextSize = 128 << 10
)

// Protector seals values with one deployment-owned AES-256-GCM key.
type Protector struct {
	aead cipher.AEAD
}

// New decodes a strict base64 AES-256 key. An empty key creates an unavailable
// protector so installations without protected state need no key.
func New(encodedKey string) (*Protector, error) {
	p := &Protector{}
	if encodedKey == "" {
		return p, nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	defer clear(key)
	if err != nil || len(key) != 32 {
		return nil, ErrUnavailable
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	p.aead, err = cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	return p, nil
}

// Available reports whether protected values can be sealed and opened.
func (p *Protector) Available() bool {
	return p != nil && p.aead != nil
}

// Seal returns version || nonce || ciphertext using caller-owned AAD.
func (p *Protector) Seal(plaintext, aad []byte) ([]byte, error) {
	if !p.Available() || len(plaintext) > maxPlaintextSize {
		return nil, ErrUnavailable
	}
	prefix := make([]byte, 1+p.aead.NonceSize())
	prefix[0] = formatVersion
	if _, err := rand.Read(prefix[1:]); err != nil {
		return nil, ErrUnavailable
	}
	return p.aead.Seal(prefix, prefix[1:], plaintext, aad), nil
}

// Open authenticates one versioned payload using caller-owned AAD.
func (p *Protector) Open(payload, aad []byte) ([]byte, error) {
	if !p.Available() ||
		len(payload) < 1+p.aead.NonceSize()+p.aead.Overhead() ||
		len(payload) > maxPlaintextSize+1+p.aead.NonceSize()+p.aead.Overhead() ||
		payload[0] != formatVersion {
		return nil, ErrUnavailable
	}
	nonceEnd := 1 + p.aead.NonceSize()
	plaintext, err := p.aead.Open(nil, payload[1:nonceEnd], payload[nonceEnd:], aad)
	if err != nil {
		return nil, ErrUnavailable
	}
	return plaintext, nil
}
