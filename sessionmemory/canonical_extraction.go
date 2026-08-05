package sessionmemory

import "context"

// CanonicalExtractionRequest is the bounded input to a v2 semantic extractor.
// The model may propose semantics and evidence, but scope and all persistent
// identifiers remain supplied by the validated terminal turn.
type CanonicalExtractionRequest struct {
	SchemaVersion string        `json:"schema_version"`
	Derivation    DerivationRef `json:"derivation"`
	Turn          Turn          `json:"turn"`
}

// Validate verifies an extraction request is grounded in one exact turn.
func (r CanonicalExtractionRequest) Validate() error {
	if r.SchemaVersion != CanonicalSchemaVersionV1 {
		return invalidDerived("canonical extraction schema version is invalid")
	}
	if err := r.Derivation.Validate(); err != nil {
		return err
	}
	return r.Turn.Validate()
}

// CanonicalSemanticExtractor produces untrusted v2 semantic candidates. The
// canonical processor validates every candidate, derives identities, and
// commits the resulting mutation atomically.
type CanonicalSemanticExtractor interface {
	ExtractCanonicalSemantics(ctx context.Context, request CanonicalExtractionRequest) ([]SemanticCandidate, error)
}

// CanonicalSemanticOperationID derives a versioned idempotency key distinct
// from the legacy atom-extraction stage, allowing cutover replay without
// colliding with existing SQLite operation records.
func CanonicalSemanticOperationID(exportID string, derivation DerivationRef) (string, error) {
	if !isCanonicalID(exportID) {
		return "", invalidDerived("canonical extraction export id is required")
	}
	if err := derivation.Validate(); err != nil {
		return "", err
	}
	return reconciliationID("operation", "canonical-semantics", exportID, derivation.Pipeline, derivation.Policy, derivation.Prompt, derivation.Model), nil
}
