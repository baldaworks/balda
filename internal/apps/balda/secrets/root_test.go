package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestScopedDerivationKnownAnswerAndUniqueRegistry(t *testing.T) {
	rootKey := bytes.Repeat([]byte{1}, 32)
	want := map[Scope]string{
		ScopeMCPRevision:      "343b469fc3269645f897b3949c34f459f65dbea4f86131e550d0e6ac5c4aca8f",
		ScopeMCPGrant:         "493e6c761e6dcb0e0a73842a027484dd14f0e3cbb9af936c91bff7bd1bd3c526",
		ScopeProviderRevision: "957a40a6b434b88b45e8239e6d680fbf5a7b9ce89e068241d91ac00aa42bd16e",
	}
	seenScopes := make(map[Scope]bool)
	seenKeys := make(map[string]bool)
	for _, scope := range Scopes() {
		if scope == "" || !strings.HasSuffix(string(scope), "/v1") || seenScopes[scope] {
			t.Fatalf("invalid or duplicate registered scope %q", scope)
		}
		seenScopes[scope] = true
		key, err := deriveKey(rootKey, scope)
		if err != nil {
			t.Fatal(err)
		}
		got := hex.EncodeToString(key)
		clear(key)
		if got != want[scope] {
			t.Fatalf("deriveKey(%q) = %s, want %s", scope, got, want[scope])
		}
		if seenKeys[got] {
			t.Fatalf("scope %q collides with another derived key", scope)
		}
		seenKeys[got] = true
	}
	if len(seenScopes) != len(want) {
		t.Fatalf("registered scopes = %d, want %d", len(seenScopes), len(want))
	}
	if _, err := deriveKey(rootKey, Scope("balda/unregistered/v1")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unregistered derivation error = %v", err)
	}
}

func TestScopedProtectorWritesV2AndSeparatesScopeAADAndRoot(t *testing.T) {
	root := newTestRoot(t, 1)
	otherRoot := newTestRoot(t, 2)
	aad := []byte("[\"identity\",\"revision\"]")
	plaintext := []byte("protected value")

	for _, scope := range Scopes() {
		protector, err := root.Protector(scope)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := protector.Seal(plaintext, aad)
		if err != nil {
			t.Fatal(err)
		}
		if payload[0] != scopedFormatVersion {
			t.Fatalf("scope %q frame version = %d, want %d", scope, payload[0], scopedFormatVersion)
		}
		got, err := protector.Open(payload, aad)
		if err != nil || !bytes.Equal(got, plaintext) {
			t.Fatalf("scope %q Open() = %q, %v", scope, got, err)
		}
		if _, err := protector.Open(payload, []byte("wrong aad")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("scope %q accepted wrong AAD: %v", scope, err)
		}
		otherProtector, err := otherRoot.Protector(scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := otherProtector.Open(payload, aad); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("scope %q accepted wrong root: %v", scope, err)
		}
		for _, otherScope := range Scopes() {
			if otherScope == scope {
				continue
			}
			otherScopeProtector, err := root.Protector(otherScope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := otherScopeProtector.Open(payload, aad); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("scope %q payload opened in %q: %v", scope, otherScope, err)
			}
		}
	}
}

func TestScopedProtectorOpensFrozenV1Frame(t *testing.T) {
	const legacyHex = "010303030303030303030303035edccb663b4c3b3009627927c9168a7885539286cef4e01940b0a58bc18925dfd0c4040fe89ec632abfc6978f09d92c7dc06acaba752ed020743b163a3"
	root := newTestRoot(t, 7)
	protector, err := root.Protector(ScopeMCPRevision)
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("[\"balda-mcp-revision-v1\",\"connection\",\"revision\"]")
	plaintext := []byte("{\"headers\":{\"Authorization\":\"legacy-secret\"}}")
	legacy := legacyFixture(t, bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{3}, 12), plaintext, aad)
	if got := hex.EncodeToString(legacy); got != legacyHex {
		t.Fatalf("legacy fixture = %s, want %s", got, legacyHex)
	}
	got, err := protector.Open(legacy, aad)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("Open(v1) = %q, %v", got, err)
	}
	if _, err := protector.Open(legacy, []byte("balda-provider-revision-v1")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("v1 frame accepted cross-scope AAD: %v", err)
	}
}

func TestScopedProtectorRejectsMalformedFrames(t *testing.T) {
	root := newTestRoot(t, 1)
	protector, err := root.Protector(ScopeMCPGrant)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := protector.Seal([]byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), payload...)
	corrupt[len(corrupt)-1] ^= 1
	oversized := make([]byte, maxPlaintextSize+1+protector.aead.NonceSize()+protector.aead.Overhead()+1)
	oversized[0] = scopedFormatVersion
	for name, candidate := range map[string][]byte{
		"empty":           nil,
		"unknown version": {99},
		"truncated v1":    {legacyFormatVersion},
		"truncated v2":    {scopedFormatVersion},
		"corrupt":         corrupt,
		"oversized":       oversized,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := protector.Open(candidate, []byte("aad")); got != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Open() = %x, %v", got, err)
			}
		})
	}
}

func TestRootRejectsUnknownScopeAndDoesNotGenerateMissingKey(t *testing.T) {
	root, err := NewRoot("")
	if err != nil || root.Available() {
		t.Fatalf("NewRoot(empty) = %#v, %v", root, err)
	}
	protector, err := root.Protector(ScopeMCPRevision)
	if err != nil || protector.Available() {
		t.Fatalf("Protector() = %#v, %v", protector, err)
	}
	if _, err := protector.Seal(nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable Seal() error = %v", err)
	}
	if _, err := root.Protector(Scope("balda/unknown/v1")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown scope error = %v", err)
	}
	if _, err := NewRoot("invalid"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid root error = %v", err)
	}
}

func newTestRoot(t *testing.T, value byte) *Root {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
	root, err := NewRoot(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func legacyFixture(t *testing.T, key, nonce, plaintext, aad []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	prefix := append([]byte{legacyFormatVersion}, nonce...)
	return aead.Seal(prefix, nonce, plaintext, aad)
}
