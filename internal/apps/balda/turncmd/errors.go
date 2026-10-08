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
