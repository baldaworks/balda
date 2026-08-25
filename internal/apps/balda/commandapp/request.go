package commandapp

import (
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// Request is the transport-neutral command ingress contract.
// Concrete transports map their local command event shape into this request
// before invoking shared command semantics.
type Request struct {
	Locator         deliverycmd.Locator
	DeliveryOptions deliveryfmt.Options
	Transport       string
	ChatID          int64
	TopicID         int
	UserID          int64
	Command         string
	Args            string
	IsDM            bool
}
