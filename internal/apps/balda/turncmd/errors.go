package turncmd

import "errors"

// ErrWebhookTurnClaimUnavailable means provider execution has not started and may retry.
var ErrWebhookTurnClaimUnavailable = errors.New("webhook turn claim unavailable")

// ErrWebhookTurnAlreadyClaimed means a provider invocation may still be active or its outcome is unknown.
var ErrWebhookTurnAlreadyClaimed = errors.New("webhook provider outcome unknown after interrupted execution")

// PreparationError identifies a failure before the provider executor was called.
// Its cause is for operator diagnostics, never for transport delivery.
type PreparationError struct {
	Cause error
}

func (e *PreparationError) Error() string { return e.Cause.Error() }
func (e *PreparationError) Unwrap() error { return e.Cause }

// FinalOutcomeAcceptedError identifies an error after the normal final response
// was accepted by delivery. Its cause remains available to operator diagnostics;
// retrying conversational provider work would repeat an already handled outcome.
type FinalOutcomeAcceptedError struct {
	Cause error
}

func (e *FinalOutcomeAcceptedError) Error() string { return e.Cause.Error() }
func (e *FinalOutcomeAcceptedError) Unwrap() error { return e.Cause }

// FinalArchiveError stops model retries after producing an unwritten final.
// The in-memory candidate was retried; a process crash cannot recover its bytes.
type FinalArchiveError struct{ Cause error }

func (e *FinalArchiveError) Error() string { return "final archive commit failed" }
func (e *FinalArchiveError) Unwrap() error { return e.Cause }
