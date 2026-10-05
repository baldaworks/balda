package mcpruntime

import (
	"errors"
	"fmt"
)

// FailureReason is a bounded launch diagnostic, never an upstream error string.
type FailureReason string

const (
	FailureUnavailable            FailureReason = "unavailable"
	FailureAuthorizationRequired  FailureReason = "authorization_required"
	FailureAuthorizationChallenge FailureReason = "authorization_challenge"
	FailureCaptureRequired        FailureReason = "capture_required"
)

// LaunchError carries trusted evidence from a resolver or concrete transport.
// Startup recovery policy belongs to the catalog/application, not the mechanism.
type LaunchError struct{ Reason FailureReason }

func (e *LaunchError) Error() string {
	return "MCP launch unavailable: " + string(boundedReason(e.Reason))
}

// AttachmentError identifies one exact failed attachment without private input.
type AttachmentError struct {
	Key    InstanceKey
	Reason FailureReason
}

func (e *AttachmentError) Error() string {
	return fmt.Sprintf("MCP attachment unavailable (%s): %s", e.Key.Name, boundedReason(e.Reason))
}

func boundedReason(reason FailureReason) FailureReason {
	switch reason {
	case FailureAuthorizationRequired, FailureAuthorizationChallenge, FailureCaptureRequired:
		return reason
	default:
		return FailureUnavailable
	}
}

func launchReason(err error) FailureReason {
	var failure *LaunchError
	if errors.As(err, &failure) {
		return boundedReason(failure.Reason)
	}
	return FailureUnavailable
}

func attachmentFailure(key InstanceKey, current *managedInstance) error {
	reason := FailureUnavailable
	if current != nil && current.health.State == HealthFailed {
		reason = boundedReason(current.failure)
	}
	return &AttachmentError{Key: key, Reason: reason}
}
