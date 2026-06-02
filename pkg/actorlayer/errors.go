package actorlayer

import "errors"

// ErrActorNotFound reports that an actor address could not be resolved.
var ErrActorNotFound = errors.New("actor not found")
