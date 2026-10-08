// Package webhookcmd defines transport-neutral webhook intake and receipts.
package webhookcmd

import (
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/destinationcmd"
)

// Request is the normalized invocation accepted by webhook application policy.
type Request struct {
	RequestID string
	RouteName string
	Prompt    string
	ReportTo  *destinationcmd.Target
	DedupeKey string

	// Target, FallbackTo and Mode are removed when private admission replaces
	// the old webhook service in TASK-007. The receiver never populates them.
	Target     destinationcmd.Target
	FallbackTo *destinationcmd.Target
	Mode       string
}

// Result is the stable acceptance receipt returned to the HTTP receiver.
type Result struct {
	RequestID string
	MessageID string
	Duplicate bool
	JobID     string
	Stream    string
	Sequence  uint64

	// Target and FallbackUsed leave with the old service in TASK-007.
	Target       destinationcmd.Resolved
	FallbackUsed bool
}

// TargetNotFoundError means a configured destination is unavailable.
type TargetNotFoundError struct{ Cause error }

func (e *TargetNotFoundError) Error() string {
	if e == nil || e.Cause == nil {
		return "target not found"
	}
	return e.Cause.Error()
}

func (e *TargetNotFoundError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsTargetNotFound(err error) bool {
	var targetErr *TargetNotFoundError
	return errors.As(err, &targetErr)
}

// QueueFullError means durable publication is under backpressure.
type QueueFullError struct{ Cause error }

func (e *QueueFullError) Error() string {
	if e == nil || e.Cause == nil {
		return "command queue is full"
	}
	return e.Cause.Error()
}

func (e *QueueFullError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsQueueFull(err error) bool {
	var queueErr *QueueFullError
	return errors.As(err, &queueErr)
}

// DispatchFailedError means admission or durable publication failed.
type DispatchFailedError struct{ Cause error }

func (e *DispatchFailedError) Error() string {
	if e == nil || e.Cause == nil {
		return "dispatch failed"
	}
	return e.Cause.Error()
}

func (e *DispatchFailedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsDispatchFailed(err error) bool {
	var dispatchErr *DispatchFailedError
	return errors.As(err, &dispatchErr)
}

// InvalidRequestError identifies invalid normalized input.
type InvalidRequestError struct {
	Field   string
	Message string
}

func (e *InvalidRequestError) Error() string {
	if e == nil {
		return "invalid request"
	}
	if e.Field != "" {
		return fmt.Sprintf("invalid request: %s: %s", e.Field, e.Message)
	}
	return fmt.Sprintf("invalid request: %s", e.Message)
}

func IsInvalidRequest(err error) bool {
	var invalidErr *InvalidRequestError
	return errors.As(err, &invalidErr)
}
