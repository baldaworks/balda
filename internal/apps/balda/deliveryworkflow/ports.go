package deliveryworkflow

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
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

type Dispatcher interface {
	Dispatch(ctx context.Context, payload deliverycmd.Payload) (string, error)
}
