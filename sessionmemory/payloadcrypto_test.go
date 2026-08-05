package sessionmemory

import (
	"context"
	"errors"
	"testing"
)

const testPrivatePayload = "private content"

func TestPayloadEncryptionRoundTripAndMissingKeyFailsClosed(t *testing.T) {
	provider := testPayloadKeyProvider{active: PayloadKey{ID: "kek-1", Material: make([]byte, 32)}}
	for index := range provider.active.Material {
		provider.active.Material[index] = byte(index + 1)
	}
	encrypted, ref, err := SealPayload(context.Background(), provider, "payload-1", []byte(testPrivatePayload))
	if err != nil {
		t.Fatalf("SealPayload() error = %v", err)
	}
	if string(encrypted.Ciphertext) == testPrivatePayload || encrypted.PayloadHash != ref.Digest {
		t.Fatalf("encrypted payload = %#v", encrypted)
	}
	if ref.ID != "payload-1" {
		t.Fatalf("payload ref ID = %q, want payload-1", ref.ID)
	}
	plaintext, err := OpenPayload(context.Background(), provider, "payload-1", encrypted, ref)
	if err != nil || string(plaintext) != testPrivatePayload {
		t.Fatalf("OpenPayload() = %q, error = %v", plaintext, err)
	}
	if _, err := OpenPayload(context.Background(), testPayloadKeyProvider{}, "payload-1", encrypted, ref); err == nil {
		t.Fatal("OpenPayload() succeeded without a KEK")
	}
	if _, err := OpenPayload(context.Background(), provider, "payload-2", encrypted, ref); err == nil {
		t.Fatal("OpenPayload() succeeded with a different payload identity")
	}
}

func TestPayloadEncryptionRewrapsDEKWithoutChangingCiphertext(t *testing.T) {
	oldKey := PayloadKey{ID: "kek-old", Material: make([]byte, 32)}
	newKey := PayloadKey{ID: "kek-new", Material: make([]byte, 32)}
	for index := range oldKey.Material {
		oldKey.Material[index] = byte(index + 1)
		newKey.Material[index] = byte(255 - index)
	}
	provider := rotatingPayloadKeyProvider{active: oldKey, keys: map[string]PayloadKey{oldKey.ID: oldKey, newKey.ID: newKey}}
	encrypted, ref, err := SealPayload(context.Background(), provider, "payload-1", []byte(testPrivatePayload))
	if err != nil {
		t.Fatalf("SealPayload() error = %v", err)
	}
	originalCiphertext := append([]byte(nil), encrypted.Ciphertext...)
	provider.active = newKey
	rotated, rotatedRef, err := RewrapPayloadDEK(context.Background(), provider, encrypted, ref)
	if err != nil || rotated.KeyID != newKey.ID || rotatedRef.KeyID != newKey.ID {
		t.Fatalf("RewrapPayloadDEK() = %#v, %#v, error = %v", rotated, rotatedRef, err)
	}
	if string(rotated.Ciphertext) != string(originalCiphertext) {
		t.Fatal("rewrap changed ciphertext")
	}
	plaintext, err := OpenPayload(context.Background(), provider, "payload-1", rotated, rotatedRef)
	if err != nil || string(plaintext) != testPrivatePayload {
		t.Fatalf("OpenPayload() after rewrap = %q, error = %v", plaintext, err)
	}
}

type testPayloadKeyProvider struct{ active PayloadKey }

func (p testPayloadKeyProvider) ActivePayloadKey(context.Context) (PayloadKey, error) {
	if p.active.ID == "" {
		return PayloadKey{}, errors.New("no active key")
	}
	return p.active, nil
}

func (p testPayloadKeyProvider) PayloadKey(_ context.Context, keyID string) (PayloadKey, error) {
	if p.active.ID != keyID {
		return PayloadKey{}, errors.New("key unavailable")
	}
	return p.active, nil
}

type rotatingPayloadKeyProvider struct {
	active PayloadKey
	keys   map[string]PayloadKey
}

func (p rotatingPayloadKeyProvider) ActivePayloadKey(context.Context) (PayloadKey, error) {
	return p.active, nil
}

func (p rotatingPayloadKeyProvider) PayloadKey(_ context.Context, keyID string) (PayloadKey, error) {
	key, ok := p.keys[keyID]
	if !ok {
		return PayloadKey{}, errors.New("key unavailable")
	}
	return key, nil
}
