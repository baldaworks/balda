package handlers

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/sessionturn"
	"github.com/normahq/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type sessionProgressUpdate = sessionturnapp.SessionProgressUpdate

type providerTurnExecutor struct {
	execution  *sessionturnapp.TurnExecutionService
	dispatcher actortransport.Dispatcher
	jobEvents  jobEventRecorder
	logger     zerolog.Logger
}

type jobEventRecorder interface {
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

func (e *providerTurnExecutor) ExecuteSessionTurn(ctx context.Context, request sessionturn.Request) error {
	if e.execution != nil {
		return sessionturnapp.NewProviderTurnExecutorFromService(e.execution).ExecuteSessionTurn(ctx, request)
	}
	return sessionturnapp.NewProviderTurnExecutor(e.dispatcher, e.jobEvents, e.logger).ExecuteSessionTurn(ctx, request)
}

type sessionTurnSessionAccessor struct {
	manager *baldasession.Manager
}

func (a sessionTurnSessionAccessor) GetSession(locator sessionturn.SessionLocator) (sessionturn.ActiveSession, error) {
	return sessionturnapp.NewSessionAccessor(a.manager).GetSession(locator)
}

func (a sessionTurnSessionAccessor) RestoreSession(ctx context.Context, sessionCtx sessionturn.SessionContext) (sessionturn.ActiveSession, error) {
	return sessionturnapp.NewSessionAccessor(a.manager).RestoreSession(ctx, sessionCtx)
}

func (a sessionTurnSessionAccessor) EnsureSession(ctx context.Context, sessionCtx sessionturn.SessionContext, agentName string) (sessionturn.ActiveSession, error) {
	return sessionturnapp.NewSessionAccessor(a.manager).EnsureSession(ctx, sessionCtx, agentName)
}

func newSessionProgressDispatcher(
	dispatcher actortransport.Dispatcher,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	jobID string,
	topicID int,
	policy deliveryfmt.ProgressPolicy,
	failHard bool,
	logger zerolog.Logger,
) sessionturnapp.SessionProgressEmitter {
	return sessionturnapp.NewSessionProgressDispatcher(dispatcher, from, locator, jobID, topicID, policy, failHard, logger)
}
