package balda

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/actors"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	natsbus "github.com/baldaworks/balda/internal/apps/balda/eventbus/nats"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/internalmcp"
	"github.com/baldaworks/balda/internal/apps/balda/jobexec"
	baldajobs "github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/scheduledjobs"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/internal/apps/balda/shutdown"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/baldaworks/balda/internal/apps/balda/webhookfx"
	portableapp "github.com/baldaworks/balda/sessionmemory/app"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/runtime"
	"go.uber.org/fx"
)

type lifecycleStage struct {
	name  string
	start func(context.Context) error
	stop  func(context.Context) error
}

const sharedHTTPIngressStage = "shared HTTP ingress"

type applicationLifecycle struct {
	logger zerolog.Logger
	stages []lifecycleStage

	mu      sync.Mutex
	started int
}

func newApplicationLifecycle(logger zerolog.Logger, stages []lifecycleStage) *applicationLifecycle {
	return &applicationLifecycle{
		logger: logger.With().Str("component", "balda.lifecycle").Logger(),
		stages: append([]lifecycleStage(nil), stages...),
	}
}

func (l *applicationLifecycle) Start(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started != 0 {
		return nil
	}

	for i, stage := range l.stages {
		if stage.start != nil {
			l.logger.Debug().Str("stage", stage.name).Msg("starting application lifecycle stage")
			if err := stage.start(ctx); err != nil {
				rollbackErr := l.stopStarted(ctx, i)
				return errors.Join(fmt.Errorf("start %s: %w", stage.name, err), rollbackErr)
			}
		}
		l.started = i + 1
	}
	return nil
}

func (l *applicationLifecycle) Stop(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopStarted(ctx, l.started)
}

func (l *applicationLifecycle) stopStarted(ctx context.Context, count int) error {
	var errs []error
	for i := count - 1; i >= 0; i-- {
		stage := l.stages[i]
		if stage.stop == nil {
			continue
		}
		l.logger.Debug().Str("stage", stage.name).Msg("stopping application lifecycle stage")
		if err := stage.stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stop %s: %w", stage.name, err))
		}
	}
	l.started = 0
	return errors.Join(errs...)
}

type telegramLifecycle struct {
	enabled    bool
	source     runtime.UpdateSource
	run        func(context.Context) error
	shutdowner fx.Shutdowner
	logger     zerolog.Logger

	cancel context.CancelFunc
	done   chan struct{}
}

func (t *telegramLifecycle) Start(ctx context.Context) error {
	if !t.enabled {
		return nil
	}
	if _, ok := t.source.(tgbotkit.SharedWebhookSource); ok {
		// The shared socket is already serving. Register synchronously so an
		// upstream failure unwinds that listener during startup.
		if err := t.source.Start(ctx); err != nil {
			return fmt.Errorf("register Telegram webhook: %w", err)
		}
	}
	runCtx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.done = make(chan struct{})
	go func() {
		defer close(t.done)
		if err := t.run(runCtx); err != nil {
			if shutdown.IsExpected(err) {
				t.logger.Debug().Err(err).Msg("telegram runtime stopped during shutdown")
				return
			}
			t.logger.Error().Err(err).Msg("telegram runtime stopped")
			if t.shutdowner != nil {
				if shutdownErr := t.shutdowner.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
					t.logger.Error().Err(shutdownErr).Msg("request Balda shutdown after Telegram runtime failure")
				}
			}
		}
	}()
	return nil
}

func (t *telegramLifecycle) Stop(ctx context.Context) error {
	if t.cancel == nil {
		return nil
	}
	t.cancel()
	select {
	case <-t.done:
		t.cancel = nil
		t.done = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type applicationLifecycleParams struct {
	fx.In

	LC                   fx.Lifecycle
	Shutdowner           fx.Shutdowner
	Logger               zerolog.Logger
	Config               BaldaConfig
	HTTPConfig           ResolvedHTTPConfig
	HTTPRegistry         *httpfx.Registry
	WebhookHTTP          webhookfx.HTTPContribution
	GatewayCallbacks     []httpfx.GatewayCallbackProvider `group:"balda_http_gateway_callback_providers"`
	TelegramSource       runtime.UpdateSource
	Backoffice           *backoffice.Runtime
	BackofficeMCP        *mcpbackofficeapp.Operations
	MCP                  *internalmcp.InternalMCPManager
	MCPManagement        *mcpmanage.Service
	MCPBridge            *mcpbridge.Bridge
	MCPAuthorizations    *mcpmanage.Authorizations
	StateProvider        state.Provider
	Catalog              *catalogapp.Lifecycle
	CatalogRuntime       *catalogapp.Runtime
	Runtime              *baldaagent.RuntimeManager
	Sessions             *session.Manager
	Bus                  *natsbus.Bus
	SessionMemoryIngress *sessionmemoryapp.IngressOutboxPublisher
	SessionMemoryWorker  *sessionmemoryapp.Worker
	SessionMemoryRuntime *portableapp.Runtime
	QuestionProjector    *questions.DeliveryBindingProjector
	Projector            *baldajobs.EventProjector
	OutboxPublisher      *baldajobs.OutboxPublisher
	ActorHost            *baldaexecution.ActorHost
	TurnDispatcher       *actors.TurnDispatcher
	Scheduler            *scheduledjobs.ScheduledJobScheduler
	WebhookRunFinalizer  *jobexec.WebhookRunFinalizer
	TransportStages      []appports.TransportLifecycleStage `group:"balda_transport_lifecycle_stage"`
	TelegramBot          *runtime.Bot
	TelegramEnabled      bool `name:"balda_telegram_enabled"`
}

func registerApplicationLifecycle(p applicationLifecycleParams) {
	telegram := &telegramLifecycle{
		enabled:    p.TelegramEnabled,
		source:     p.TelegramSource,
		run:        p.TelegramBot.Run,
		shutdowner: p.Shutdowner,
		logger:     p.Logger.With().Str("component", "balda.telegram_runtime").Logger(),
	}
	coordinator := newApplicationLifecycle(p.Logger, applicationLifecycleStages(p, telegram))
	p.LC.Append(fx.Hook{OnStart: coordinator.Start, OnStop: coordinator.Stop})
}

func applicationLifecycleStages(p applicationLifecycleParams, telegram *telegramLifecycle) []lifecycleStage {
	var sharedServer *httpfx.Server
	stages := []lifecycleStage{
		// FX constructs this before Start. Register cleanup first so every failed
		// startup stage also closes pending browser authorization attempts.
		{name: "MCP authorization attempts", stop: func(context.Context) error { p.MCPAuthorizations.Close(); return nil }},
		{name: "user readiness", start: p.Backoffice.ValidateReady},
		{name: "bundled MCP", start: p.MCP.EnsureStarted, stop: p.MCP.Stop},
		{name: "managed MCP credential readiness", start: func(ctx context.Context) error {
			return p.MCPManagement.ValidateCredentials(ctx, p.StateProvider.MCP())
		}},
		{name: "MCP credential bridge", start: p.MCPBridge.Start, stop: p.MCPBridge.Close},
		{name: "runtime contribution catalog", start: p.Catalog.Start, stop: p.Catalog.Stop},
		{name: "session-memory runtime", start: func(ctx context.Context) error {
			return startSessionMemoryRuntime(ctx, p.SessionMemoryRuntime)
		}, stop: func(ctx context.Context) error {
			return closeSessionMemoryRuntime(ctx, p.SessionMemoryRuntime)
		}},
		{name: "provider runtime", start: func(ctx context.Context) error {
			err := p.Runtime.EnsureRuntime(ctx)
			if err != nil && p.CatalogRuntime.MCPAuthorizationPending(ctx, err) {
				p.Logger.Warn().Msg("provider unavailable until MCP worker authorization completes")
				return nil
			}
			return err
		}, stop: p.Runtime.Stop},
		{name: "session manager", start: p.Sessions.Start, stop: p.Sessions.Stop},
		{name: "durable transport", start: p.Bus.Start, stop: p.Bus.Drain},
		{name: "session-memory ingress outbox", start: p.SessionMemoryIngress.Start, stop: p.SessionMemoryIngress.Stop},
		{name: "session memory", start: p.SessionMemoryWorker.Start, stop: func(ctx context.Context) error {
			// Persist the final boundary while the ingress publisher and durable
			// transport are still live, then drain the serialized provider
			// consumer. This stage is intentionally before the stop-only turn
			// dispatcher so reverse shutdown stops turn production first.
			return errors.Join(p.Sessions.PublishShutdownBoundaries(ctx), p.SessionMemoryWorker.Stop(ctx))
		}},
		{name: "turn dispatcher", stop: p.TurnDispatcher.Shutdown},
		{name: "question delivery binding projector", start: p.QuestionProjector.Start, stop: p.QuestionProjector.Stop},
		{name: "job event projector", start: p.Projector.Start, stop: p.Projector.Stop},
		{name: "job event outbox", start: p.OutboxPublisher.Start, stop: p.OutboxPublisher.Stop},
		{name: "actor host", start: p.ActorHost.Start, stop: p.ActorHost.Stop},
		{name: "webhook run finalizer", start: p.WebhookRunFinalizer.Start, stop: p.WebhookRunFinalizer.Stop},
		{name: "scheduled jobs", start: p.Scheduler.Start, stop: p.Scheduler.Stop},
		{name: sharedHTTPIngressStage, start: func(ctx context.Context) error {
			server, err := startSharedHTTPIngress(ctx, p)
			if err != nil {
				return err
			}
			sharedServer = server
			go func() {
				<-sharedServer.Done()
				if err := sharedServer.Err(); err != nil {
					p.Logger.Error().Err(err).Msg("shared HTTP ingress stopped unexpectedly")
					if shutdownErr := p.Shutdowner.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
						p.Logger.Error().Err(shutdownErr).Msg("request Balda shutdown after shared HTTP failure")
					}
				}
			}()
			return nil
		}, stop: func(ctx context.Context) error {
			if sharedServer == nil {
				return nil
			}
			return sharedServer.Stop(ctx)
		}},
	}
	for _, stage := range p.TransportStages {
		stages = append(stages, lifecycleStage{
			name:  stage.Name,
			start: stage.Start,
			stop:  stage.Stop,
		})
	}
	stages = append(stages, lifecycleStage{name: "telegram ingress", start: telegram.Start, stop: telegram.Stop})
	return stages
}

func startSharedHTTPIngress(ctx context.Context, p applicationLifecycleParams) (*httpfx.Server, error) {
	handler, err := backofficeSharedHandler(ctx, p.Backoffice, p.Config, p.BackofficeMCP)
	if err != nil {
		return nil, err
	}
	if err := p.HTTPRegistry.AddBackoffice("backoffice", handler); err != nil {
		return nil, err
	}
	for _, provider := range p.GatewayCallbacks {
		callbacks, err := provider(ctx)
		if err != nil {
			return nil, fmt.Errorf("prepare gateway callbacks: %w", err)
		}
		if err := p.HTTPRegistry.AddGatewayCallbacks(callbacks); err != nil {
			return nil, err
		}
	}
	if err := p.WebhookHTTP(ctx, p.HTTPRegistry); err != nil {
		return nil, fmt.Errorf("prepare generic webhooks: %w", err)
	}
	if source, ok := p.TelegramSource.(tgbotkit.SharedWebhookSource); ok {
		if err := source.UseSharedListener(); err != nil {
			return nil, fmt.Errorf("prepare Telegram webhook source: %w", err)
		}
	}
	server := httpfx.NewServer(p.HTTPConfig.ListenAddr, p.HTTPRegistry.Handler())
	if err := server.Start(); err != nil {
		return nil, err
	}
	return server, nil
}

type sessionMemoryRuntime interface {
	Start(ctx context.Context) error
	Close(ctx context.Context) error
}

func startSessionMemoryRuntime(ctx context.Context, runtime sessionMemoryRuntime) error {
	if runtime == nil {
		return nil
	}
	return runtime.Start(ctx)
}

func closeSessionMemoryRuntime(ctx context.Context, runtime sessionMemoryRuntime) error {
	if runtime == nil {
		return nil
	}
	return runtime.Close(ctx)
}
