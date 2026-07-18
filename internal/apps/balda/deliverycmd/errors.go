package deliverycmd

import "errors"

// ErrorKind describes whether a concrete channel delivery failure can be
// retried without changing the requested audience or payload semantics.
type ErrorKind string

const (
	ErrorKindRetryable ErrorKind = "retryable"
	ErrorKindPermanent ErrorKind = "permanent"
)

// DeliveryError carries transport-neutral retry semantics across the channel
// adapter boundary.
type DeliveryError struct {
	Kind ErrorKind
	Err  error
}

func (e *DeliveryError) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Kind)
	}
	return e.Err.Error()
}

func (e *DeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func RetryableError(err error) error {
	return deliveryError(ErrorKindRetryable, err)
}

func PermanentError(err error) error {
	return deliveryError(ErrorKindPermanent, err)
}

// ClassifyError returns the explicit channel delivery classification. The bool
// is false for legacy/unclassified adapter errors.
func ClassifyError(err error) (ErrorKind, bool) {
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) || deliveryErr.Kind == "" {
		return "", false
	}
	return deliveryErr.Kind, true
}

func deliveryError(kind ErrorKind, err error) error {
	if err == nil {
		err = errors.New(string(kind) + " delivery error")
	}
	return &DeliveryError{Kind: kind, Err: err}
}
