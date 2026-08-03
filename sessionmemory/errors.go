package sessionmemory

import (
	"errors"
	"fmt"
	"strings"
)

// ErrorCode is a stable provider and adapter outcome code.
type ErrorCode string

const (
	// CodeDisabled means session memory is not enabled.
	CodeDisabled ErrorCode = "disabled"
	// CodeInvalidScope means a locator scope is malformed.
	CodeInvalidScope ErrorCode = "invalid_scope"
	// CodeUnsupportedScope means no safe classifier exists for a locator.
	CodeUnsupportedScope ErrorCode = "unsupported_scope"
	// CodeInvalidSession means stable session identity is missing or malformed.
	CodeInvalidSession ErrorCode = "invalid_session"
	// CodeInvalidQuery means a search query or limit is invalid.
	CodeInvalidQuery ErrorCode = "invalid_query"
	// CodeScopeViolation means a provider returned data for another scope.
	CodeScopeViolation ErrorCode = "scope_violation"
	// CodeUnavailable means a provider is temporarily unavailable.
	CodeUnavailable ErrorCode = "unavailable"
	// CodeTimeout means a bounded provider operation timed out.
	CodeTimeout ErrorCode = "timeout"
	// CodePermanent means a provider rejected an operation permanently.
	CodePermanent ErrorCode = "permanent"
	// CodeShuttingDown means the integration is no longer accepting work.
	CodeShuttingDown ErrorCode = "shutting_down"
	// CodeInvalidDerived means derived memory or provenance is malformed.
	CodeInvalidDerived ErrorCode = "invalid_derived"
	// CodeConflict means optimistic concurrency or revision state changed.
	CodeConflict ErrorCode = "conflict"
	// CodeNotFound means a requested source or revision does not exist.
	CodeNotFound ErrorCode = "not_found"
	// CodeForgotten means content was removed by a forgetting operation.
	CodeForgotten ErrorCode = "forgotten"
	// CodeLimitExceeded means a derived-memory hard bound was exceeded.
	CodeLimitExceeded ErrorCode = "limit_exceeded"
	// CodeStoreFailure means the persistence port failed.
	CodeStoreFailure ErrorCode = "store_failure"
	// CodeModelFailure means a model port failed.
	CodeModelFailure ErrorCode = "model_failure"
)

// ErrorClass controls whether a failed operation may be retried.
type ErrorClass string

const (
	// ErrorClassRetryable permits a bounded retry with the same idempotency key.
	ErrorClassRetryable ErrorClass = "retryable"
	// ErrorClassPermanent forbids automatic retry of the same invalid request.
	ErrorClassPermanent ErrorClass = "permanent"
)

// Error is a stable, classifiable session-memory failure.
type Error struct {
	Code    ErrorCode
	Class   ErrorClass
	Message string
	Cause   error
}

// Error returns a diagnostic message without changing the stable code.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = string(e.Code)
	}
	if e.Cause == nil {
		return message
	}
	return fmt.Sprintf("%s: %v", message, e.Cause)
}

// Unwrap returns the underlying provider or validation failure.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// RetryableError creates a retryable failure with a stable code.
func RetryableError(code ErrorCode, message string, cause error) error {
	return newError(code, ErrorClassRetryable, message, cause)
}

// PermanentError creates a non-retryable failure with a stable code.
func PermanentError(code ErrorCode, message string, cause error) error {
	return newError(code, ErrorClassPermanent, message, cause)
}

// ClassifyError returns the stable code and retry class carried by err.
func ClassifyError(err error) (ErrorCode, ErrorClass, bool) {
	var target *Error
	if !errors.As(err, &target) || target == nil || target.Code == "" || target.Class == "" {
		return "", "", false
	}
	return target.Code, target.Class, true
}

// IsRetryable reports whether err explicitly permits retry.
func IsRetryable(err error) bool {
	_, class, ok := ClassifyError(err)
	return ok && class == ErrorClassRetryable
}

func newError(code ErrorCode, class ErrorClass, message string, cause error) error {
	return &Error{Code: code, Class: class, Message: strings.TrimSpace(message), Cause: cause}
}
