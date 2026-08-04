package deliveryworkflow

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type DeliveryStore interface {
	ReserveDelivery(ctx context.Context, record baldastate.DeliveryRecord) (baldastate.DeliveryRecord, bool, error)
	MarkDeliverySending(ctx context.Context, deliveryKey string) error
	MarkDeliveryFailed(ctx context.Context, deliveryKey string, reason string) error
	MarkDeliverySent(ctx context.Context, deliveryKey string, providerMessageID string) error
}

type JobEvents interface {
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

// Delivery is the process-local request handed to the concrete delivery edge.
// Message is present only for registry-formatted model content and is never
// serialized into the durable delivery command.
type Delivery struct {
	Payload deliverycmd.Payload
	Message *deliveryfmt.Message
}

type Dispatcher interface {
	Dispatch(ctx context.Context, delivery Delivery) (string, error)
}
