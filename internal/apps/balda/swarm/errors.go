package swarm

import (
	"errors"
	"fmt"

	actorengine "github.com/normahq/norma/pkg/actorlayer/engine"
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

func classifyError(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var actorErr *ActorError
	if errors.As(err, &actorErr) && actorErr.Kind != "" {
		return actorErr.Kind
	}
	return ErrorKindTransient
}

func ClassifyError(err error) ErrorKind {
	return classifyError(err)
}

// IsRetryableError reports whether an error should be retried by runtime consumers.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, actorengine.ErrActorNotFound) {
		return false
	}
	switch ClassifyError(err) {
	case ErrorKindPolicy, ErrorKindPermanent, ErrorKindDecode:
		return false
	default:
		return true
	}
}
