package jobs

import (
	"context"
	"fmt"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"go.uber.org/fx"
)

type JobEventsService struct {
	store ServiceStore
	bus   actortransport.EventPublisher
}

type jobEventsServiceParams struct {
	fx.In

	Store ServiceStore
	Bus   actortransport.EventPublisher `optional:"true"`
}

func NewJobEventsService(params jobEventsServiceParams) (*JobEventsService, error) {
	if params.Store == nil {
		return nil, fmt.Errorf("job events store is required")
	}
	return &JobEventsService{store: params.Store, bus: params.Bus}, nil
}

func (s *JobEventsService) AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error {
	if s == nil {
		return nil
	}
	event, err := jobEventRecord(jobID, eventType, actor, messageID, payload)
	if err != nil {
		return err
	}
	outbox, err := jobEventOutboxRecord(event)
	if err != nil {
		return err
	}
	if err := s.store.EnqueueJobEvent(ctx, outbox); err != nil {
		return err
	}
	s.publishOutboxBestEffort(ctx, outbox)
	return nil
}

func (s *JobEventsService) publishOutboxBestEffort(ctx context.Context, record baldastate.JobEventOutboxRecord) {
	if s == nil {
		return
	}
	publishOutboxBestEffort(ctx, s.store, s.bus, record)
}
