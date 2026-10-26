package sessionturnapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type ProviderTurnExecutor struct {
	execution  *TurnExecutionService
	dispatcher actortransport.Dispatcher
	jobEvents  jobEventAppender
	logger     zerolog.Logger
}

type leasedTurnRuntimeState struct {
	runtime   sessionturn.RuntimeView
	writer    archiveRuntimeStateWriter
	locator   session.SessionLocator
	archiveID string
}

func (s leasedTurnRuntimeState) RuntimeStateValue(ctx context.Context, key string) (any, bool, error) {
	return s.runtime.RuntimeStateValue(ctx, key)
}

func (s leasedTurnRuntimeState) UpdateRuntimeState(ctx context.Context, state map[string]any) error {
	if len(state) == 0 {
		return nil
	}
	if s.writer == nil {
		return fmt.Errorf("update leased runtime state: %w", chatarchivecmd.ErrUnavailable)
	}
	updated, err := s.writer.UpdateRuntimeStateForArchive(ctx, s.locator, s.archiveID, state)
	if err != nil {
		return fmt.Errorf("update leased runtime state: %w", err)
	}
	if !updated {
		return fmt.Errorf("update leased runtime state: %w", chatarchivecmd.ErrConflict)
	}
	return nil
}

func NewProviderTurnExecutorFromService(execution *TurnExecutionService) *ProviderTurnExecutor {
	if execution == nil {
		return &ProviderTurnExecutor{}
	}
	return &ProviderTurnExecutor{
		execution:  execution,
		dispatcher: execution.dispatcher,
		jobEvents:  execution.jobEvents,
		logger:     execution.logger,
	}
}

func NewProviderTurnExecutor(dispatcher actortransport.Dispatcher, jobEvents jobEventAppender, logger zerolog.Logger) *ProviderTurnExecutor {
	return &ProviderTurnExecutor{
		dispatcher: dispatcher,
		jobEvents:  jobEvents,
		logger:     logger,
	}
}

func (e *ProviderTurnExecutor) ExecuteSessionTurn(ctx context.Context, request sessionturn.Request) error {
	execution := e.execution
	if execution == nil {
		execution = &TurnExecutionService{
			dispatcher:   e.dispatcher,
			jobEvents:    e.jobEvents,
			logger:       e.logger,
			autoMaxTurns: automode.DefaultMaxTurns,
		}
	}
	payload := request.Payload
	if request.Runtime == nil {
		return session.ErrRuntimeUnavailable
	}
	providerRunner := request.Runtime.Runner()
	runtimeState := leasedTurnRuntimeState{
		runtime: request.Runtime,
		writer:  execution.runtimeStateWriter,
		locator: session.SessionLocator{
			SessionID:   payload.Locator.SessionID,
			ChannelType: payload.Locator.ChannelType,
			AddressKey:  payload.Locator.AddressKey,
			AddressJSON: payload.Locator.AddressJSON,
		},
		archiveID: request.Runtime.ArchiveID(),
	}
	from := actorlayer.ActorAddress{Target: actorcmd.ActorTypeSession, Key: request.Session.GetSessionID()}
	var progressEmitter SessionProgressEmitter
	privateRun := turncmd.IsPrivateRun(payload.Source, request.Session.GetSessionID())
	executionLocator := request.DeliveryLocator
	if privateRun {
		executionLocator = sessionturn.SessionLocator{
			SessionID:   payload.Locator.SessionID,
			ChannelType: payload.Locator.ChannelType,
			AddressKey:  payload.Locator.AddressKey,
			AddressJSON: payload.Locator.AddressJSON,
		}
	}
	if payload.Deliver && !privateRun {
		progressEmitter = NewSessionProgressDispatcher(
			execution.dispatcher,
			from,
			session.SessionLocator{
				SessionID:   request.DeliveryLocator.SessionID,
				ChannelType: request.DeliveryLocator.ChannelType,
				AddressKey:  request.DeliveryLocator.AddressKey,
				AddressJSON: request.DeliveryLocator.AddressJSON,
			},
			payload.JobID,
			payload.TopicID,
			request.DeliveryOptions.ProgressPolicy,
			strings.TrimSpace(payload.JobID) != "",
			execution.progressHook,
			execution.logger,
		)
	}
	workspaceDir := ""
	if workspace, ok := request.Session.(interface{ GetWorkspaceDir() string }); ok {
		workspaceDir = workspace.GetWorkspaceDir()
	}
	return execution.Execute(ctx, ExecutionRequest{
		ArchiveID:        request.Runtime.ArchiveID(),
		SteeringMessages: payload.SteeringMessages,
		WorkspaceDir:     workspaceDir,
		Text:             payload.Text,
		Runner:           providerRunner,
		UserID:           request.Runtime.UserID(),
		RequesterUserID:  payload.RequesterUserID,
		SessionID:        request.Session.GetSessionID(),
		JobID:            payload.JobID,
		AgentSessionID:   request.Runtime.SessionID(),
		Locator: session.SessionLocator{
			SessionID:   executionLocator.SessionID,
			ChannelType: executionLocator.ChannelType,
			AddressKey:  executionLocator.AddressKey,
			AddressJSON: executionLocator.AddressJSON,
		},
		DeliveryLocator: session.SessionLocator{
			SessionID:   request.DeliveryLocator.SessionID,
			ChannelType: request.DeliveryLocator.ChannelType,
			AddressKey:  request.DeliveryLocator.AddressKey,
			AddressJSON: request.DeliveryLocator.AddressJSON,
		},
		Attachments:     attachment.NormalizeList(payload.Attachments),
		MessageID:       payload.MessageID,
		DeliveryOptions: request.DeliveryOptions,
		Deliver:         payload.Deliver,
		ProgressEmitter: progressEmitter,
		OutboundFrom:    from,
		RunOptions:      request.MemoryRunOptions,
		MemoryRefresh:   request.MemoryRefresh,
		SelectedSkill:   request.SelectedSkill,
		TurnSource:      payload.Source,
		DedupeKey:       payload.DedupeKey,
		RuntimeState:    runtimeState,
	})
}

var _ sessionturn.Executor = (*ProviderTurnExecutor)(nil)
