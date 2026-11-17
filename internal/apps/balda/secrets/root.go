package secrets

import (
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
)

// Scope is a stable, versioned domain-separation label. Scope values are
// persisted compatibility contracts and must never be renamed or reused.
type Scope string

const (
	ScopeMCPRevision      Scope = "balda/mcp/revision/v1"
	ScopeMCPGrant         Scope = "balda/mcp/grant/v1"
	ScopeProviderRevision Scope = "balda/provider/revision/v1"
)

const derivationSalt = "balda/application-encryption/v1"

var registeredScopes = [...]Scope{
	ScopeMCPRevision,
	ScopeMCPGrant,
	ScopeProviderRevision,
}

// Root owns the sole application encryption root and its registered scoped
// protectors. It retains AEAD instances, not decoded or derived key buffers.
type Root struct {
	legacy     cipher.AEAD
	protectors map[Scope]*Protector
}

// NewRoot decodes one strict base64 32-byte root and eagerly constructs every
// registered scoped protector. Empty material produces unavailable protectors
// and never generates an implicit key.
func NewRoot(encodedKey string) (*Root, error) {
	root := &Root{protectors: make(map[Scope]*Protector, len(registeredScopes))}
	if encodedKey == "" {
		for _, scope := range registeredScopes {
			root.protectors[scope] = &Protector{sealVersion: scopedFormatVersion}
		}
		return root, nil
	}
	rootKey, err := decodeRootKey(encodedKey)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer clear(rootKey)
	legacy, err := newAEAD(rootKey)
	if err != nil {
		return nil, ErrUnavailable
	}
	root.legacy = legacy
	for _, scope := range registeredScopes {
		derived, err := deriveKey(rootKey, scope)
		if err != nil {
			return nil, ErrUnavailable
		}
		aead, aeadErr := newAEAD(derived)
		clear(derived)
		if aeadErr != nil {
			return nil, ErrUnavailable
		}
		root.protectors[scope] = &Protector{aead: aead, legacyAEAD: legacy, sealVersion: scopedFormatVersion}
	}
	return root, nil
}

// Available reports whether the root can protect application data.
func (r *Root) Available() bool {
	return r != nil && r.legacy != nil
}

// Protector returns the registered scoped protector. Persisted input can never
// select a scope; trusted wiring chooses it before data reaches this boundary.
func (r *Root) Protector(scope Scope) (*Protector, error) {
	if r == nil {
		return nil, ErrUnavailable
	}
	protector, ok := r.protectors[scope]
	if !ok {
		return nil, ErrUnavailable
	}
	return protector, nil
}

// Scopes returns a copy of the registered compatibility labels.
func Scopes() []Scope {
	return append([]Scope(nil), registeredScopes[:]...)
}

func deriveKey(rootKey []byte, scope Scope) ([]byte, error) {
	if !registeredScope(scope) {
		return nil, ErrUnavailable
	}
	key, err := hkdf.Key(sha256.New, rootKey, []byte(derivationSalt), string(scope), 32)
	if err != nil {
		return nil, ErrUnavailable
	}
	return key, nil
}

func registeredScope(scope Scope) bool {
	for _, candidate := range registeredScopes {
		if candidate == scope {
			return true
		}
	}
	return false
}
