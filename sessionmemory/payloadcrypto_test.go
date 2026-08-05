package sessionmemory

import (
	"context"
	"errors"
	"testing"
)

func TestPayloadEncryptionRoundTripAndMissingKeyFailsClosed(t *testing.T) {
	provider := testPayloadKeyProvider{active: PayloadKey{ID: "kek-1", Material: make([]byte, 32)}}
	for index := range provider.active.Material {
		provider.active.Material[index] = byte(index + 1)
	}
	encrypted, ref, err := SealPayload(context.Background(), provider, "payload-1", []byte("private content"))
	if err != nil {
		t.Fatalf("SealPayload() error = %v", err)
	}
	if string(encrypted.Ciphertext) == "private content" || encrypted.PayloadHash != ref.Digest {
		t.Fatalf("encrypted payload = %#v", encrypted)
	}
	plaintext, err := OpenPayload(context.Background(), provider, "payload-1", encrypted, ref)
	if err != nil || string(plaintext) != "private content" {
		t.Fatalf("OpenPayload() = %q, error = %v", plaintext, err)
	}
	if _, err := OpenPayload(context.Background(), testPayloadKeyProvider{}, "payload-1", encrypted, ref); err == nil {
		t.Fatal("OpenPayload() succeeded without a KEK")
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
