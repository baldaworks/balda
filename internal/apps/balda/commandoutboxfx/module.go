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
	fx.Annotate(func(d *commandoutbox.Dispatcher) actortransport.Dispatcher { return d }, fx.ResultTags(`name:"command_ingress"`)),
	fx.Annotate(func(d *commandoutbox.Dispatcher) chatapp.Dispatcher { return d }, fx.ResultTags(`name:"command_ingress"`)),
	fx.Annotate(func(d *commandoutbox.Dispatcher) commandfx.CommandDispatcher { return d }, fx.ResultTags(`name:"command_ingress"`)),
	fx.Annotate(func(d *commandoutbox.Dispatcher) handlersfx.QuestionContinuationDispatcher { return d }, fx.ResultTags(`name:"command_ingress"`)),
), fx.Invoke(func(*questionoutbox.Runner) {}))

// NewAnswerRecovery binds persisted question handoffs to the shared outbox.
func NewAnswerRecovery(lc fx.Lifecycle, source *questions.Service, d *commandoutbox.Dispatcher, logger zerolog.Logger) (*questionoutbox.Runner, error) {
	r, err := questionoutbox.New(source, d, logger)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStart: func(context.Context) error { r.Start(); return nil }, OnStop: r.Stop})
	return r, nil
}

// NewDispatcher composes local storage, publication and the host lifecycle.
func NewDispatcher(lc fx.Lifecycle, provider state.Provider, downstream actortransport.Dispatcher, logger zerolog.Logger) (*commandoutbox.Dispatcher, error) {
	d, err := commandoutbox.NewDispatcher(provider.AppKV(), downstream, logger)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStart: func(context.Context) error { d.Start(); return nil }, OnStop: d.Stop})
	return d, nil
}
