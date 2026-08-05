package sessionmemory

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// PayloadKeyProvider supplies active and historical KEKs. Implementations
// must never persist or log the returned key material.
type PayloadKeyProvider interface {
	ActivePayloadKey(ctx context.Context) (PayloadKey, error)
	PayloadKey(ctx context.Context, keyID string) (PayloadKey, error)
}

// PayloadKey is a 32-byte AES-256 key identified by an application-owned ID.
type PayloadKey struct {
	ID       string
	Material []byte
}

// EncryptedPayload stores ciphertext and a wrapped per-payload DEK. It is a
// blob record, not part of any structural source/message/revision record.
type EncryptedPayload struct {
	KeyID       string `json:"key_id"`
	Nonce       []byte `json:"nonce"`
	Ciphertext  []byte `json:"ciphertext"`
	DEKNonce    []byte `json:"dek_nonce"`
	WrappedDEK  []byte `json:"wrapped_dek"`
	PayloadHash string `json:"payload_hash"`
}

func (k PayloadKey) validate() error {
	if !isCanonicalID(k.ID) || len(k.Material) != 32 {
		return invalidDerived("payload key is invalid")
	}
	return nil
}

// SealPayload encrypts plaintext with a random DEK and wraps that DEK with
// the active KEK. Associated data binds the ciphertext to its PayloadRef ID.
func SealPayload(ctx context.Context, provider PayloadKeyProvider, payloadID string, plaintext []byte) (EncryptedPayload, PayloadRef, error) {
	if ctx == nil || provider == nil || !isCanonicalID(payloadID) || len(plaintext) == 0 {
		return EncryptedPayload{}, PayloadRef{}, invalidDerived("payload encryption input is invalid")
	}
	key, err := provider.ActivePayloadKey(ctx)
	if err != nil {
		return EncryptedPayload{}, PayloadRef{}, RetryableError(CodeStoreFailure, "load active payload key", err)
	}
	if err := key.validate(); err != nil {
		return EncryptedPayload{}, PayloadRef{}, err
	}
	dek := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return EncryptedPayload{}, PayloadRef{}, RetryableError(CodeStoreFailure, "generate payload DEK", err)
	}
	nonce, ciphertext, err := sealAESGCM(dek, plaintext, []byte(payloadID))
	if err != nil {
		return EncryptedPayload{}, PayloadRef{}, err
	}
	dekNonce, wrappedDEK, err := sealAESGCM(key.Material, dek, []byte(key.ID))
	if err != nil {
		return EncryptedPayload{}, PayloadRef{}, err
	}
	digest := sha256.Sum256(plaintext)
	ref := PayloadRef{ID: payloadID, KeyID: key.ID, Digest: hex.EncodeToString(digest[:]), ByteSize: uint32(len(plaintext))}
	return EncryptedPayload{KeyID: key.ID, Nonce: nonce, Ciphertext: ciphertext, DEKNonce: dekNonce, WrappedDEK: wrappedDEK, PayloadHash: ref.Digest}, ref, nil
}

// OpenPayload decrypts a payload only if its KEK is available and every
// integrity check succeeds. Missing key material fails closed.
func OpenPayload(ctx context.Context, provider PayloadKeyProvider, payloadID string, encrypted EncryptedPayload, ref PayloadRef) ([]byte, error) {
	if ctx == nil || provider == nil || !isCanonicalID(payloadID) || payloadID != ref.ID || ref.Validate() != nil || encrypted.KeyID != ref.KeyID || encrypted.PayloadHash != ref.Digest {
		return nil, PermanentError(CodeStoreFailure, "encrypted payload metadata is invalid", nil)
	}
	key, err := provider.PayloadKey(ctx, encrypted.KeyID)
	if err != nil {
		return nil, PermanentError(CodeStoreFailure, "required payload key is unavailable", err)
	}
	if err := key.validate(); err != nil || key.ID != encrypted.KeyID {
		return nil, PermanentError(CodeStoreFailure, "required payload key is invalid", err)
	}
	dek, err := openAESGCM(key.Material, encrypted.DEKNonce, encrypted.WrappedDEK, []byte(key.ID))
	if err != nil {
		return nil, PermanentError(CodeStoreFailure, "unwrap payload DEK", err)
	}
	plaintext, err := openAESGCM(dek, encrypted.Nonce, encrypted.Ciphertext, []byte(payloadID))
	if err != nil {
		return nil, PermanentError(CodeStoreFailure, "decrypt payload", err)
	}
	digest := sha256.Sum256(plaintext)
	if len(plaintext) != int(ref.ByteSize) || hex.EncodeToString(digest[:]) != ref.Digest {
		return nil, PermanentError(CodeStoreFailure, "payload integrity check failed", nil)
	}
	return plaintext, nil
}

// RewrapPayloadDEK rotates the KEK wrapping a payload's DEK without reading
// the payload plaintext or changing its ciphertext/digest.
func RewrapPayloadDEK(ctx context.Context, provider PayloadKeyProvider, encrypted EncryptedPayload, ref PayloadRef) (EncryptedPayload, PayloadRef, error) {
	if ctx == nil || provider == nil || ref.Validate() != nil || encrypted.KeyID != ref.KeyID || encrypted.PayloadHash != ref.Digest {
		return EncryptedPayload{}, PayloadRef{}, PermanentError(CodeStoreFailure, "encrypted payload metadata is invalid", nil)
	}
	oldKey, err := provider.PayloadKey(ctx, encrypted.KeyID)
	if err != nil {
		return EncryptedPayload{}, PayloadRef{}, PermanentError(CodeStoreFailure, "required payload key is unavailable", err)
	}
	if err := oldKey.validate(); err != nil || oldKey.ID != encrypted.KeyID {
		return EncryptedPayload{}, PayloadRef{}, PermanentError(CodeStoreFailure, "required payload key is invalid", err)
	}
	activeKey, err := provider.ActivePayloadKey(ctx)
	if err != nil {
		return EncryptedPayload{}, PayloadRef{}, RetryableError(CodeStoreFailure, "load active payload key", err)
	}
	if err := activeKey.validate(); err != nil {
		return EncryptedPayload{}, PayloadRef{}, err
	}
	if activeKey.ID == oldKey.ID {
		return encrypted, ref, nil
	}
	dek, err := openAESGCM(oldKey.Material, encrypted.DEKNonce, encrypted.WrappedDEK, []byte(oldKey.ID))
	if err != nil {
		return EncryptedPayload{}, PayloadRef{}, PermanentError(CodeStoreFailure, "unwrap payload DEK", err)
	}
	dekNonce, wrappedDEK, err := sealAESGCM(activeKey.Material, dek, []byte(activeKey.ID))
	if err != nil {
		return EncryptedPayload{}, PayloadRef{}, err
	}
	encrypted.KeyID = activeKey.ID
	encrypted.DEKNonce = dekNonce
	encrypted.WrappedDEK = wrappedDEK
	ref.KeyID = activeKey.ID
	return encrypted, ref, nil
}

func sealAESGCM(key, plaintext, additionalData []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate AES-GCM nonce: %w", err)
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, additionalData), nil
}

func openAESGCM(key, nonce, ciphertext, additionalData []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, PermanentError(CodeStoreFailure, "payload nonce is invalid", nil)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}
