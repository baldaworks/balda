package swarm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type EventProjector struct {
	consumer EventConsumer
	store    baldastate.SwarmStore
	logger   zerolog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type eventProjectorParams struct {
	fx.In

	LC            fx.Lifecycle
	Consumer      EventConsumer
	StateProvider baldastate.Provider
	Logger        zerolog.Logger
}

func NewEventProjector(params eventProjectorParams) (*EventProjector, error) {
	if params.StateProvider == nil {
		return nil, fmt.Errorf("balda state provider is required")
	}
	if params.Consumer == nil {
		return nil, fmt.Errorf("event projector requires an actor runtime event consumer")
	}
	p := &EventProjector{
		consumer: params.Consumer,
		store:    params.StateProvider.Swarm(),
		logger:   params.Logger.With().Str("component", "balda.swarm.event_projector").Logger(),
	}
	params.LC.Append(fx.Hook{OnStart: p.Start, OnStop: p.Stop})
	return p, nil
}

func (p *EventProjector) Start(context.Context) error {
	if p == nil || p.consumer == nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if err := p.consumer.RunEventConsumer(runCtx, p.Project); err != nil && !errors.Is(err, context.Canceled) {
			p.logger.Error().Err(err).Msg("event projector stopped")
		}
	}()
	return nil
}

func (p *EventProjector) Stop(ctx context.Context) error {
	if p == nil || p.cancel == nil {
		return nil
	}
	p.cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.wg.Wait()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *EventProjector) Project(ctx context.Context, subject string, env Envelope) error {
	if p == nil || p.store == nil {
		return nil
	}
	taskID := strings.TrimSpace(env.TaskID)
	if taskID == "" {
		return nil
	}
	eventType := ""
	if env.Meta != nil {
		if value := strings.TrimSpace(env.Meta["event_type"]); value != "" {
			eventType = value
		}
	}
	if eventType == "" {
		switch strings.TrimSpace(subject) {
		case SubjectEventCommandAccepted:
			eventType = "command.accepted"
		case SubjectEventCommandRunning:
			eventType = "command.running"
		case SubjectEventCommandInProgress:
			eventType = "command.in_progress"
		case SubjectEventCommandAcked:
			eventType = "command.acked"
		case SubjectEventCommandRetrying:
			eventType = "command.retrying"
		case SubjectEventCommandDeadLettered:
			eventType = "command.deadlettered"
		case SubjectEventCommandNoop:
			eventType = "command.noop"
		case SubjectEventCommandDecodeFailed:
			eventType = "command.decode_failed"
		case SubjectEventTaskCreated:
			eventType = TaskEventTaskCreated
		case SubjectEventTaskUpdated:
			eventType = TaskEventTaskAssigned
		case SubjectEventTaskCompleted:
			eventType = TaskEventTaskCompleted
		case SubjectEventDeliverySent:
			eventType = TaskEventDeliverySent
		case SubjectEventDeliveryFailed:
			eventType = TaskEventDeliveryFailed
		}
	}
	if eventType == "" {
		return nil
	}
	actor := strings.TrimSpace(env.Meta["actor"])
	if actor == "" {
		if from, err := env.From.String(); err == nil {
			actor = from
		}
	}
	messageID := strings.TrimSpace(env.Meta["message_id"])
	if messageID == "" {
		messageID = strings.TrimSpace(env.CausationID)
	}
	return p.store.AppendTaskEvent(ctx, baldastate.SwarmTaskEventRecord{
		ID:          strings.TrimSpace(env.ID),
		TaskID:      taskID,
		EventType:   eventType,
		Actor:       actor,
		MessageID:   messageID,
		PayloadJSON: strings.TrimSpace(env.PayloadJSON),
	})
}
