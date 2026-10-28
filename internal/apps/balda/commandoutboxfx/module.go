// Package commandoutboxfx binds durable command admission to host capabilities.
package commandoutboxfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/handlersfx"
	"github.com/baldaworks/balda/internal/apps/balda/jobs/commandoutbox"
	"github.com/baldaworks/balda/internal/apps/balda/jobs/questionoutbox"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

// Module supplies shared durable admission to chat, commands and question continuations.
var Module = fx.Module("balda_command_outbox", fx.Provide(
	NewDispatcher,
	NewAnswerRecovery,
	NewLifecycleWorkers,
	fx.Annotate(func(d *commandoutbox.Dispatcher) actortransport.Dispatcher { return d }, fx.ResultTags(`name:"command_ingress"`)),
	fx.Annotate(func(d *commandoutbox.Dispatcher) chatapp.Dispatcher { return d }, fx.ResultTags(`name:"command_ingress"`)),
	fx.Annotate(func(d *commandoutbox.Dispatcher) commandfx.CommandDispatcher { return d }, fx.ResultTags(`name:"command_ingress"`)),
	fx.Annotate(func(d *commandoutbox.Dispatcher) handlersfx.QuestionContinuationDispatcher { return d }, fx.ResultTags(`name:"command_ingress"`)),
), fx.Invoke(func(*questionoutbox.Runner) {}))

// LifecycleWorkers exposes command recovery workers to the central application
// lifecycle without leaking their concrete job packages across the composition boundary.
type LifecycleWorkers struct {
	dispatcher *commandoutbox.Dispatcher
	recovery   *questionoutbox.Runner
}

// NewLifecycleWorkers groups workers that share the application state provider.
func NewLifecycleWorkers(dispatcher *commandoutbox.Dispatcher, recovery *questionoutbox.Runner) *LifecycleWorkers {
	return &LifecycleWorkers{dispatcher: dispatcher, recovery: recovery}
}

func (w *LifecycleWorkers) StartDispatcher() { w.dispatcher.Start() }

func (w *LifecycleWorkers) StopDispatcher(ctx context.Context) error { return w.dispatcher.Stop(ctx) }

func (w *LifecycleWorkers) StartAnswerRecovery() { w.recovery.Start() }

func (w *LifecycleWorkers) StopAnswerRecovery(ctx context.Context) error { return w.recovery.Stop(ctx) }

// NewAnswerRecovery binds persisted question handoffs to the shared outbox.
func NewAnswerRecovery(source *questions.Service, d *commandoutbox.Dispatcher, logger zerolog.Logger) (*questionoutbox.Runner, error) {
	return questionoutbox.New(source, d, logger)
}

// NewDispatcher composes local storage and publication. The central application
// lifecycle starts it only after user readiness succeeds.
func NewDispatcher(provider state.Provider, downstream actortransport.Dispatcher, logger zerolog.Logger) (*commandoutbox.Dispatcher, error) {
	return commandoutbox.NewDispatcher(provider.AppKV(), downstream, logger)
}
