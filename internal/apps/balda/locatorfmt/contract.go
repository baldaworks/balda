// Package locatorfmt defines the transport-neutral structured contract for
// presenting public Balda locator references.
package locatorfmt

import "github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"

// Response contains the semantic values shown by a successful locator command.
// Transport renderers own all labels and presentation markup.
type Response struct {
	Transport string
	Locator   string
}

// ResponseDescriptor identifies the versioned structured locator response.
var ResponseDescriptor = deliveryfmt.Descriptor[Response]{
	Type: "balda.locator.response.v1",
}
