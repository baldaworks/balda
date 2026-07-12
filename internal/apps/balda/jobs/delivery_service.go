package jobs

import (
	"context"
	"fmt"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

type DeliveryService struct {
	store ServiceStore
}

type deliveryServiceParams struct {
	fx.In

	Store ServiceStore
}

func NewDeliveryService(params deliveryServiceParams) (*DeliveryService, error) {
	if params.Store == nil {
		return nil, fmt.Errorf("delivery store is required")
	}
	return &DeliveryService{store: params.Store}, nil
}

func (s *DeliveryService) ReserveDelivery(ctx context.Context, record baldastate.DeliveryRecord) (baldastate.DeliveryRecord, bool, error) {
	if s == nil {
		return baldastate.DeliveryRecord{}, false, nil
	}
	return s.store.ReserveDelivery(ctx, record)
}

func (s *DeliveryService) MarkDeliverySent(ctx context.Context, deliveryKey string, providerMessageID string) error {
	if s == nil {
		return nil
	}
	return s.store.MarkDeliverySent(ctx, deliveryKey, providerMessageID)
}

func (s *DeliveryService) MarkDeliverySending(ctx context.Context, deliveryKey string) error {
	if s == nil {
		return nil
	}
	return s.store.MarkDeliverySending(ctx, deliveryKey)
}

func (s *DeliveryService) MarkDeliveryFailed(ctx context.Context, deliveryKey string, reason string) error {
	if s == nil {
		return nil
	}
	return s.store.MarkDeliveryFailed(ctx, deliveryKey, reason)
}
