package sessionmemory

import (
	"context"
	"errors"
)

// ProcessingOperationID derives the stable idempotency key for one typed
// processing stage. It remains part of the portable contract used by the
// canonical processor and boundary application.
func ProcessingOperationID(stage OperationStage, exportID string, derivations ...DerivationRef) (string, error) {
	if err := stage.Validate(); err != nil {
		return "", err
	}
	if !isCanonicalID(exportID) {
		return "", invalidDerived("processing export id is required")
	}
	derivation := LegacyDerivationRef()
	if len(derivations) > 1 {
		return "", invalidDerived("one derivation reference is allowed")
	}
	if len(derivations) == 1 {
		derivation = derivations[0]
	}
	if err := derivation.Validate(); err != nil {
		return "", err
	}
	if derivation == LegacyDerivationRef() {
		return derivedStableID("operation", string(stage), exportID), nil
	}
	return derivedStableID("operation", string(stage), exportID, derivation.Pipeline, derivation.Policy, derivation.Prompt, derivation.Model), nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return invalidDerived("context is required")
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return RetryableError(CodeTimeout, "derived memory operation timed out", nil)
		}
		return RetryableError(CodeTimeout, "derived memory operation was canceled", nil)
	}
	return nil
}

func turnTextExceeds(turn Turn, limit int) bool {
	remaining := limit
	for _, message := range turn.Messages {
		if len(message.Text) > remaining {
			return true
		}
		remaining -= len(message.Text)
	}
	return false
}

func validateCommittedOutcome(request CommitRequest, outcome OperationOutcome) error {
	if err := outcome.Validate(); err != nil {
		return err
	}
	if outcome.OperationID != request.OperationID || outcome.Stage != request.Stage || outcome.Scope != request.Scope {
		return PermanentError(CodeScopeViolation, "canonical commit outcome does not match the operation", nil)
	}
	if outcome.ScopeVersion != request.ExpectedScopeVersion+1 {
		return PermanentError(CodeConflict, "canonical commit returned an unexpected scope version", nil)
	}
	if len(outcome.Revisions) != len(request.Atoms)+len(request.Scenarios)+len(request.Profiles) {
		return invalidDerived("canonical commit outcome revision count does not match the request")
	}
	want := make(map[RevisionRef]struct{}, len(outcome.Revisions))
	for _, atom := range request.Atoms {
		want[RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}] = struct{}{}
	}
	for _, scenario := range request.Scenarios {
		want[RevisionRef{ItemID: scenario.Meta.ItemID, RevisionID: scenario.Meta.RevisionID}] = struct{}{}
	}
	for _, profile := range request.Profiles {
		want[RevisionRef{ItemID: profile.Meta.ItemID, RevisionID: profile.Meta.RevisionID}] = struct{}{}
	}
	for _, ref := range outcome.Revisions {
		if _, ok := want[ref]; !ok {
			return invalidDerived("canonical commit outcome contains an unknown revision")
		}
		delete(want, ref)
	}
	return nil
}
