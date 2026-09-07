// Package commandfx binds CommandActor ports at the Balda composition boundary.
package commandfx

import (
	"context"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/actors/command"
	commandauto "github.com/baldaworks/balda/internal/apps/balda/actors/command/auto"
	commandcancel "github.com/baldaworks/balda/internal/apps/balda/actors/command/cancel"
	commandclose "github.com/baldaworks/balda/internal/apps/balda/actors/command/closecmd"
	commandgoalkeeper "github.com/baldaworks/balda/internal/apps/balda/actors/command/goalkeeper"
	commandhelp "github.com/baldaworks/balda/internal/apps/balda/actors/command/help"
	commandlocator "github.com/baldaworks/balda/internal/apps/balda/actors/command/locator"
	commandreset "github.com/baldaworks/balda/internal/apps/balda/actors/command/reset"
	commandstart "github.com/baldaworks/balda/internal/apps/balda/actors/command/start"
	commandtopic "github.com/baldaworks/balda/internal/apps/balda/actors/command/topic"
	commandusage "github.com/baldaworks/balda/internal/apps/balda/actors/command/usage"
	commanduser "github.com/baldaworks/balda/internal/apps/balda/actors/command/user"
	commandplugin "github.com/baldaworks/balda/internal/apps/balda/actors/command/plugin"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	"github.com/baldaworks/balda/internal/apps/balda/jobs"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionapp"
	"github.com/baldaworks/go-actorlayer/dispatch"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"go.uber.org/fx"
)

type resetParams struct {
	fx.In
	Sessions   *session.Manager
	Canceller  appports.SessionWorkCanceller
	Dispatcher actortransport.Dispatcher
}

type usageParams struct {
	fx.In
	Sessions   *session.Manager
	Dispatcher actortransport.Dispatcher
	Logger     zerolog.Logger
}

type autoParams struct {
	fx.In
	Sessions     *session.Manager
	Dispatcher   actortransport.Dispatcher
	AutoMaxTurns int `name:"balda_automode_max_turns"`
	Logger       zerolog.Logger
}

type cancelParams struct {
	fx.In
	Control    *controlapp.CommandDispatcher
	Dispatcher actortransport.Dispatcher
	Logger     zerolog.Logger
}

type goalkeeperParams struct {
	fx.In
	Control       *controlapp.CommandDispatcher
	GoalJobs      *jobs.JobLifecycleService `optional:"true"`
	Dispatcher    actortransport.Dispatcher
	MaxIterations int `name:"balda_goal_max_iterations" optional:"true"`
	Logger        zerolog.Logger
}

type topicParams struct {
	fx.In
	Sessions   *session.Manager
	TopicSvc   *sessionapp.TopicService `optional:"true"`
	Dispatcher actortransport.Dispatcher
	Logger     zerolog.Logger
}

type closeParams struct {
	fx.In
	Sessions   *session.Manager
	Canceller  appports.SessionWorkCanceller `optional:"true"`
	Control    *controlapp.CommandDispatcher `optional:"true"`
	TopicSvc   *sessionapp.TopicService      `optional:"true"`
	Dispatcher actortransport.Dispatcher
	Logger     zerolog.Logger
}

type startParams struct {
	fx.In
	OwnerStore        *auth.OwnerStore             `optional:"true"`
	InviteStore       *auth.InviteStore            `optional:"true"`
	CollaboratorStore *auth.CollaboratorStore      `optional:"true"`
	ChannelAuth       *auth.ChannelAuthService     `optional:"true"`
	Bootstrap         *sessionapp.BootstrapService `optional:"true"`
	Dispatcher        actortransport.Dispatcher
	AuthToken         string                       `name:"balda_auth_token" optional:"true"`
	Logger            zerolog.Logger
}

type userParams struct {
	fx.In
	OwnerStore        *auth.OwnerStore                    `optional:"true"`
	InviteStore       *auth.InviteStore                   `optional:"true"`
	CollaboratorStore *auth.CollaboratorStore             `optional:"true"`
	TGClient          client.ClientWithResponsesInterface `optional:"true"`
	Dispatcher        actortransport.Dispatcher
	Logger            zerolog.Logger
}

type tgBotUsernameProvider struct {
	client client.ClientWithResponsesInterface
	mu     sync.Mutex
	cached string
}

func (p *tgBotUsernameProvider) GetBotUsername(ctx context.Context) string {
	if p == nil || p.client == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != "" {
		return p.cached
	}
	resp, err := p.client.GetMeWithResponse(ctx)
	if err != nil {
		return ""
	}
	if resp.JSON200 == nil || resp.JSON200.Result.Username == nil {
		return ""
	}
	p.cached = *resp.JSON200.Result.Username
	return p.cached
}

type pluginParams struct {
	fx.In
	OwnerStore *auth.OwnerStore   `optional:"true"`
	Plugins    *pluginapp.Service `optional:"true"`
	Dispatcher actortransport.Dispatcher
	Logger     zerolog.Logger
}

var Module = fx.Module("balda_command",
	fx.Provide(
		fx.Annotate(commandlocator.New, fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`)),
		fx.Annotate(
			func(p resetParams) *commandreset.Handler {
				return commandreset.New(p.Sessions, p.Canceller, p.Dispatcher)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(commandhelp.New, fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`)),
		fx.Annotate(
			func(p usageParams) *commandusage.Handler {
				return commandusage.New(p.Sessions, p.Dispatcher, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p autoParams) *commandauto.Handler {
				return commandauto.New(p.Sessions, p.Dispatcher, p.AutoMaxTurns, nil, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p cancelParams) *commandcancel.Handler {
				return commandcancel.New(p.Control, p.Dispatcher, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p goalkeeperParams) *commandgoalkeeper.Handler {
				var checker commandgoalkeeper.GoalJobChecker
				if p.GoalJobs != nil {
					checker = p.GoalJobs
				}
				return commandgoalkeeper.New(p.Control, checker, p.Dispatcher, p.MaxIterations, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p topicParams) *commandtopic.Handler {
				return commandtopic.New(p.Sessions, p.TopicSvc, p.Dispatcher, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p closeParams) *commandclose.Handler {
				var canceller commandclose.WorkCanceller
				if p.Canceller != nil {
					canceller = p.Canceller
				}
				var control commandclose.ControlDispatcher
				if p.Control != nil {
					control = p.Control
				}
				var topicSvc commandclose.TopicCloser
				if p.TopicSvc != nil {
					topicSvc = p.TopicSvc
				}
				return commandclose.New(p.Sessions, canceller, control, topicSvc, p.Dispatcher, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p startParams) *commandstart.Handler {
				var ownerStore commandstart.OwnerStore
				if p.OwnerStore != nil {
					ownerStore = p.OwnerStore
				}
				var inviteStore commandstart.InviteStore
				if p.InviteStore != nil {
					inviteStore = p.InviteStore
				}
				var collaboratorStore commandstart.CollaboratorStore
				if p.CollaboratorStore != nil {
					collaboratorStore = p.CollaboratorStore
				}
				var channelAuth commandstart.ChannelAuthService
				if p.ChannelAuth != nil {
					channelAuth = p.ChannelAuth
				}
				var activator commandstart.OwnerActivator
				if p.Bootstrap != nil {
					activator = p.Bootstrap
				}
				return commandstart.New(ownerStore, inviteStore, collaboratorStore, channelAuth, activator, p.Dispatcher, p.AuthToken, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p userParams) *commanduser.Handler {
				var ownerStore commanduser.OwnerStore
				if p.OwnerStore != nil {
					ownerStore = p.OwnerStore
				}
				var inviteStore commanduser.InviteStore
				if p.InviteStore != nil {
					inviteStore = p.InviteStore
				}
				var collaboratorStore commanduser.CollaboratorStore
				if p.CollaboratorStore != nil {
					collaboratorStore = p.CollaboratorStore
				}
				var botUsernames commanduser.BotUsernameProvider
				if p.TGClient != nil {
					botUsernames = &tgBotUsernameProvider{client: p.TGClient}
				}
				return commanduser.New(ownerStore, inviteStore, collaboratorStore, botUsernames, p.Dispatcher, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(p pluginParams) *commandplugin.Handler {
				var ownerStore commandplugin.OwnerStore
				if p.OwnerStore != nil {
					ownerStore = p.OwnerStore
				}
				var plugins commandplugin.Service
				if p.Plugins != nil {
					plugins = p.Plugins
				}
				return commandplugin.New(ownerStore, plugins, p.Dispatcher, p.Logger)
			},
			fx.As(new(command.Handler)), fx.ResultTags(`group:"balda_command_handlers"`),
		),
		fx.Annotate(
			func(handlers []command.Handler, advertisements []commandcmd.Advertisement) (*command.Router, error) {
				router, err := command.NewRouter(handlers)
				if err != nil {
					return nil, err
				}
				for _, advertisement := range advertisements {
					if advertisement.Enabled {
						if err := router.ValidateAdvertised(advertisement.Names); err != nil {
							return nil, err
						}
					}
				}
				return router, nil
			},
			fx.ParamTags(`group:"balda_command_handlers"`, `group:"balda_command_advertisements"`),
		),
		fx.Annotate(
			func(router *command.Router) dispatch.Actor {
				return command.NewActor(router)
			},
			fx.As(new(dispatch.Actor)), fx.ResultTags(`group:"balda_product_actors"`),
		),
	),
)
