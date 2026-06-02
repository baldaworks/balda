package swarm

import "github.com/normahq/balda/pkg/actorlayer"

type ErrorKind = actorlayer.ErrorKind
type ActorError = actorlayer.ActorError

const (
	ErrorKindTransient        = actorlayer.ErrorKindTransient
	ErrorKindPolicy           = actorlayer.ErrorKindPolicy
	ErrorKindPermanent        = actorlayer.ErrorKindPermanent
	ErrorKindDecode           = actorlayer.ErrorKindDecode
	ErrorKindExternalDelivery = actorlayer.ErrorKindExternalDelivery
)

func TransientError(err error) error        { return actorlayer.TransientError(err) }
func PolicyError(err error) error           { return actorlayer.PolicyError(err) }
func PermanentError(err error) error        { return actorlayer.PermanentError(err) }
func DecodeError(err error) error           { return actorlayer.DecodeError(err) }
func ExternalDeliveryError(err error) error { return actorlayer.ExternalDeliveryError(err) }
func ClassifyError(err error) ErrorKind     { return actorlayer.ClassifyError(err) }
func IsRetryableError(err error) bool       { return actorlayer.IsRetryableError(err) }
