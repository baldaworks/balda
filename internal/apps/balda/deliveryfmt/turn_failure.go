package deliveryfmt

// TurnFailure contains the complete failed-final text, including any partial reply.
// It is rendered locally before the existing delivery envelope is published.
type TurnFailure struct {
	Text string
}

// TurnFailureDescriptor identifies process-local failed-final presentation.
var TurnFailureDescriptor = Descriptor[TurnFailure]{Type: "balda.turn.failure.v1"}
