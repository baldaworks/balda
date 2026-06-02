package actorlayer

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

type ErrorKind string

const (
	ErrorKindTransient        ErrorKind = "transient"
	ErrorKindPolicy           ErrorKind = "policy"
	ErrorKindPermanent        ErrorKind = "permanent"
	ErrorKindDecode           ErrorKind = "decode"
	ErrorKindExternalDelivery ErrorKind = "external_delivery"
)

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

func TransientError(err error) error { return actorError(ErrorKindTransient, err) }
func PolicyError(err error) error    { return actorError(ErrorKindPolicy, err) }
func PermanentError(err error) error { return actorError(ErrorKindPermanent, err) }
func DecodeError(err error) error    { return actorError(ErrorKindDecode, err) }

func ExternalDeliveryError(err error) error {
	return actorError(ErrorKindExternalDelivery, err)
}

func actorError(kind ErrorKind, err error) error {
	if err == nil {
		err = fmt.Errorf("%s error", kind)
	}
	return &ActorError{Kind: kind, Err: err}
}

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

func RetryExhausted(attempt int, maxAttempts int) bool {
	return maxAttempts > 0 && attempt >= maxAttempts
}

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
