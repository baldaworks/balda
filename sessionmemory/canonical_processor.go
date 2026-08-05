package sessionmemory

import "context"

// CanonicalPayloadSealer encrypts one payload and returns the pair that must
// be committed with the structural mutation that references it.
type CanonicalPayloadSealer interface {
	SealCanonicalPayload(ctx context.Context, payloadID string, plaintext []byte) (CanonicalPayload, error)
}

// CanonicalTurnProcessor owns the v2 turn-processing dependencies. Its
// implementation is introduced separately so composition can cut over from
// the legacy SQLite provider without leaking Badger types into the core.
type CanonicalTurnProcessor struct {
	store     CanonicalStore
	extractor CanonicalSemanticExtractor
	sealer    CanonicalPayloadSealer
	policy    PolicyRegistry
}

// NewCanonicalTurnProcessor validates the mandatory cutover dependencies.
func NewCanonicalTurnProcessor(store CanonicalStore, extractor CanonicalSemanticExtractor, sealer CanonicalPayloadSealer, policy PolicyRegistry) (*CanonicalTurnProcessor, error) {
	if store == nil || extractor == nil || sealer == nil {
		return nil, PermanentError(CodeStoreFailure, "canonical processor dependencies are required", nil)
	}
	if !isCanonicalID(policy.Version) {
		return nil, invalidDerived("canonical reconciliation policy version is required")
	}
	return &CanonicalTurnProcessor{store: store, extractor: extractor, sealer: sealer, policy: policy}, nil
}
