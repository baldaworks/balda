package actorlayer

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// ErrorKind classifies actor and delivery failures for retry decisions.
type ErrorKind string

const (
	// ErrorKindTransient marks a retryable temporary failure.
	ErrorKindTransient ErrorKind = "transient"
	// ErrorKindPolicy marks a non-retryable policy or authorization failure.
	ErrorKindPolicy ErrorKind = "policy"
	// ErrorKindPermanent marks a non-retryable permanent actor failure.
	ErrorKindPermanent ErrorKind = "permanent"
	// ErrorKindDecode marks malformed payload or envelope input.
	ErrorKindDecode ErrorKind = "decode"
	// ErrorKindExternalDelivery marks retryable infrastructure delivery failure.
	ErrorKindExternalDelivery ErrorKind = "external_delivery"
)

// ActorError wraps an error with an actorlayer classification.
type ActorError struct {
	Kind ErrorKind
	Err  error
}

func (e *ActorError) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Kind)
	}
	return e.Err.Error()
}

func (e *ActorError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// TransientError marks err as retryable.
func TransientError(err error) error { return actorError(ErrorKindTransient, err) }

// PolicyError marks err as non-retryable policy failure.
func PolicyError(err error) error { return actorError(ErrorKindPolicy, err) }

// PermanentError marks err as non-retryable permanent failure.
func PermanentError(err error) error { return actorError(ErrorKindPermanent, err) }

// DecodeError marks err as malformed input.
func DecodeError(err error) error { return actorError(ErrorKindDecode, err) }

// ExternalDeliveryError marks err as retryable delivery infrastructure failure.
func ExternalDeliveryError(err error) error {
	return actorError(ErrorKindExternalDelivery, err)
}

func actorError(kind ErrorKind, err error) error {
	if err == nil {
		err = fmt.Errorf("%s error", kind)
	}
	return &ActorError{Kind: kind, Err: err}
}

// ClassifyError returns the actorlayer error kind for err.
//
// Unclassified non-nil errors are treated as transient to preserve retry by
// default.
func ClassifyError(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var actorErr *ActorError
	if errors.As(err, &actorErr) && actorErr.Kind != "" {
		return actorErr.Kind
	}
	return ErrorKindTransient
}

// IsRetryableError reports whether err should be retried by default.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrActorNotFound) {
		return false
	}
	switch ClassifyError(err) {
	case ErrorKindPolicy, ErrorKindPermanent, ErrorKindDecode:
		return false
	default:
		return true
	}
}

// RetryExhausted reports whether the current one-based attempt has reached the
// configured max attempts.
func RetryExhausted(attempt int, maxAttempts int) bool {
	return maxAttempts > 0 && attempt >= maxAttempts
}

// RetryDelay returns an exponential retry delay with bounded jitter.
func RetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := retryBaseDelay
	for range attempt {
		delay *= 2
		if delay >= retryMaxDelay {
			delay = retryMaxDelay
			break
		}
	}
	jitterCap := max(delay/4, time.Millisecond)
	jitter := time.Duration(rand.Int64N(int64(jitterCap)))
	return delay + jitter
}

const (
	retryBaseDelay = time.Second
	retryMaxDelay  = time.Minute
)
