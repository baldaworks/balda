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
	legacyFormatVersion = byte(1)
	scopedFormatVersion = byte(2)
	maxPlaintextSize    = 128 << 10
)

// Protector seals values with one deployment-owned AES-256-GCM key.
type Protector struct {
	aead        cipher.AEAD
	legacyAEAD  cipher.AEAD
	sealVersion byte
}

// New decodes a strict base64 AES-256 key for the temporary legacy wiring. An
// empty key creates an unavailable protector so installations without protected
// state need no key.
//
// Deprecated: application consumers must use NewRoot and a registered scope.
func New(encodedKey string) (*Protector, error) {
	p := &Protector{sealVersion: legacyFormatVersion}
	if encodedKey == "" {
		return p, nil
	}
	key, err := decodeRootKey(encodedKey)
	defer clear(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	p.aead, err = newAEAD(key)
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
	prefix[0] = p.sealVersion
	if _, err := rand.Read(prefix[1:]); err != nil {
		return nil, ErrUnavailable
	}
	return p.aead.Seal(prefix, prefix[1:], plaintext, aad), nil
}

// Open authenticates one versioned payload using caller-owned AAD.
func (p *Protector) Open(payload, aad []byte) ([]byte, error) {
	if !p.Available() || len(payload) == 0 {
		return nil, ErrUnavailable
	}
	aead := p.aead
	switch payload[0] {
	case legacyFormatVersion:
		if p.legacyAEAD != nil {
			aead = p.legacyAEAD
		} else if p.sealVersion != legacyFormatVersion {
			return nil, ErrUnavailable
		}
	case scopedFormatVersion:
		if p.sealVersion != scopedFormatVersion {
			return nil, ErrUnavailable
		}
	default:
		return nil, ErrUnavailable
	}
	if len(payload) < 1+aead.NonceSize()+aead.Overhead() || len(payload) > maxPlaintextSize+1+aead.NonceSize()+aead.Overhead() {
		return nil, ErrUnavailable
	}
	nonceEnd := 1 + aead.NonceSize()
	plaintext, err := aead.Open(nil, payload[1:nonceEnd], payload[nonceEnd:], aad)
	if err != nil {
		return nil, ErrUnavailable
	}
	return plaintext, nil
}

func decodeRootKey(encodedKey string) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, ErrUnavailable
	}
	return key, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrUnavailable
	}
	return aead, nil
}
