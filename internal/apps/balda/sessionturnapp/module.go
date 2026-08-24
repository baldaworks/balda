package sessionturnapp

import (
	"context"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/sessionmemory"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type progressTransportHookParams struct {
	fx.In

	Service *TurnExecutionService
	Hook    ProgressTransportHook `optional:"true"`
}

type completedTurnCaptureAdapter struct {
	capture *sessionmemoryapp.TurnCapture
}

func (a completedTurnCaptureAdapter) CaptureCompletedTurn(ctx context.Context, turn CompletedTurn) error {
	if a.capture == nil {
		return nil
	}
	return a.capture.CaptureCompletedTurn(ctx, sessionmemoryapp.CaptureRequest{
		UserText:       turn.UserText,
		AssistantText:  turn.AssistantText,
		Locator:        turn.Locator,
		SessionID:      turn.SessionID,
		AgentSessionID: turn.AgentSessionID,
		SourceTurnID:   turn.SourceTurnID,
		CompletedAt:    turn.CompletedAt,
		TerminalStatus: sessionmemory.TurnTerminalStatus(turn.TerminalStatus),
		TrustedTools:   trustedToolEvidenceForCapture(turn.TrustedTools),
	})
}

func trustedToolEvidenceForCapture(evidence []TrustedToolEvidence) []sessionmemoryapp.TrustedToolEvidence {
	tools := make([]sessionmemoryapp.TrustedToolEvidence, 0, len(evidence))
	for _, tool := range evidence {
		tools = append(tools, sessionmemoryapp.TrustedToolEvidence{Name: tool.Name, CallID: tool.CallID, Text: tool.Text})
	}
	return tools
}

func newTurnExecutionServiceWithCapture(
	dispatcher actortransport.Dispatcher,
	jobEvents *baldajobs.JobEventsService,
	sessions *baldasession.Manager,
	logger zerolog.Logger,
	autoMaxTurns int,
	capture CompletedTurnCapture,
	registry deliveryfmt.PromptRegistry,
) *TurnExecutionService {
	return NewTurnExecutionServiceWithFormats(dispatcher, jobEvents, sessions, logger, autoMaxTurns, capture, registry)
}

var Module = fx.Module("balda_sessionturnapp",
	fx.Provide(
		fx.Annotate(newTurnExecutionServiceWithCapture, fx.ParamTags(``, ``, ``, ``, `name:"balda_automode_max_turns"`, ``, ``)),
		fx.Annotate(
			func(capture *sessionmemoryapp.TurnCapture) CompletedTurnCapture {
				return completedTurnCaptureAdapter{capture: capture}
			},
			fx.As(new(CompletedTurnCapture)),
		),
		fx.Annotate(NewProviderTurnExecutorFromService, fx.As(new(sessionturn.Executor))),
		NewSessionAccessor,
		NewMemoryStateProvider,
		sessionturn.NewRunner,
		fx.Annotate(func(r *sessionturn.Runner) appports.SessionTurnRunner { return r }),
	),
	fx.Invoke(
		func(params progressTransportHookParams) {
			params.Service.SetProgressTransportHook(params.Hook)
		},
	),
)
