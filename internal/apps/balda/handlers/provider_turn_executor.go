package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/actorcmd"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/normahq/balda/internal/apps/balda/sessionturn"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type providerTurnExecutor struct {
	dispatcher actortransport.Dispatcher
	jobEvents  jobEventAppender
	logger     zerolog.Logger
	now        func() time.Time
}

type providerTurnExecutorParams struct {
	fx.In

	Dispatcher actortransport.Dispatcher
	JobService *baldajobs.JobService `optional:"true"`
	Logger     zerolog.Logger
}

func newProviderTurnExecutor(params providerTurnExecutorParams) *providerTurnExecutor {
	return &providerTurnExecutor{
		dispatcher: params.Dispatcher,
		jobEvents:  params.JobService,
		logger:     params.Logger.With().Str("component", "balda.provider_turn_executor").Logger(),
	}
}

func (e *providerTurnExecutor) ExecuteSessionTurn(ctx context.Context, request sessionturn.Request) error {
	payload := request.Payload
	from := actorlayer.ActorAddress{Target: actorcmd.ActorTypeSession, Key: request.Session.GetSessionID()}
	handler := &BaldaHandler{
		sessionManager:  nil,
		actorDispatcher: e.dispatcher,
		jobEvents:       e.jobEvents,
		logger:          e.logger,
		now:             e.now,
		outboundFrom:    from,
	}
	handler.progressEmitter = newSessionProgressDispatcher(
		e.dispatcher,
		from,
		request.DeliveryLocator,
		payload.JobID,
		payload.TopicID,
		request.DeliveryOptions.ProgressPolicy,
		strings.TrimSpace(payload.JobID) != "",
		e.logger,
	)
	return handler.runTurnWithDeliveryOptions(
		ctx,
		payload.Text,
		request.Session.GetRunner(),
		request.UserID,
		request.Session.GetSessionID(),
		payload.JobID,
		request.AgentSessionID,
		request.DeliveryLocator,
		payload.MessageID,
		request.DeliveryOptions,
		payload.Deliver,
		request.MemoryRunOptions...,
	)
}

var _ sessionturn.Executor = (*providerTurnExecutor)(nil)
