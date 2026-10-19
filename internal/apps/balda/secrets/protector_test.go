package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"testing"
)

func TestProtectorOpensLegacyAESGCMFrame(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{3}, aead.NonceSize())
	aad := []byte(`["balda-mcp-revision-v1","connection","revision"]`)
	plaintext := []byte(`{"headers":{"Authorization":"legacy-secret"}}`)
	prefix := append([]byte{1}, nonce...)
	legacy := aead.Seal(prefix, nonce, plaintext, aad)

	p, err := New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Open(legacy, aad)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("Open() = %q, %v", got, err)
	}
}

func TestProtectorSeparatesAADAndRejectsUnavailableMaterial(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	p, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := p.Seal([]byte("secret"), []byte("mcp"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Open(payload, []byte("provider")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cross-domain payload accepted: %v", err)
	}
	payload[len(payload)-1] ^= 1
	if _, err := p.Open(payload, []byte("mcp")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("tampered payload accepted: %v", err)
	}
	empty, err := New("")
	if err != nil || empty.Available() {
		t.Fatalf("empty key = %#v, %v", empty, err)
	}
	if _, err := empty.Seal(nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable Seal() error = %v", err)
	}
	if _, err := New("invalid"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid key error = %v", err)
	}
}
