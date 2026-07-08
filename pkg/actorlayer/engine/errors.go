package engine

import (
	"fmt"

	"github.com/normahq/balda/pkg/actorlayer"
)

// ErrActorNotFound identifies delivery attempts whose target actor cannot be
// resolved.
var ErrActorNotFound = actorlayer.ErrActorNotFound

// ResolveError is returned when a dispatch address cannot be resolved.
type ResolveError struct {
	Address string
}

func (e *ResolveError) Error() string {
	return fmt.Sprintf("%s: %s", ErrActorNotFound.Error(), e.Address)
}

func (e *ResolveError) Unwrap() error {
	return ErrActorNotFound
}
