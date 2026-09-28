package mattermostfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

// CommandServerParams are the dependencies of NewCommandServer as resolved by fx.
type CommandServerParams struct {
	fx.In

	Processor  mattermost.InboundProcessor `optional:"true"`
	Client     *mattermost.Client
	Enabled    bool   `name:"balda_mattermost_commands_enabled"`
	ListenAddr string `name:"balda_mattermost_commands_listen_addr"`
	Path       string `name:"balda_mattermost_commands_path"`
	Token      string `name:"balda_mattermost_commands_token"`
	Logger     zerolog.Logger
}

// NewCommandServer builds the Mattermost slash-command HTTP receiver. The
// processor is optional so the transport can be wired in isolation (tests,
// tooling) without the full application.
func NewCommandServer(params CommandServerParams) *mattermost.CommandServer {
	return mattermost.NewCommandServer(mattermost.CommandServerParams{
		Processor: params.Processor,
		Client:    params.Client,
		Config: mattermost.CommandServerConfig{
			Enabled:    params.Enabled,
			ListenAddr: params.ListenAddr,
			Path:       params.Path,
			Token:      params.Token,
		},
		Logger: params.Logger,
	})
}

// IngressParams are the dependencies of NewIngress.
//
// Processor is optional: the transport can be constructed without the shared
// inbound pipeline (tests, tooling), matching how the other transports wire
// their optional processor dependency.
type IngressParams struct {
	fx.In

	Processor   mattermost.InboundProcessor `optional:"true"`
	Client      *mattermost.Client
	Commands    *commandcmd.Registry
	Enabled     bool   `name:"balda_mattermost_enabled"`
	BotUserID   string `name:"balda_mattermost_bot_user_id"`
	BotUsername string `name:"balda_mattermost_bot_username"`
	Logger      zerolog.Logger
}

// NewIngress builds the Mattermost websocket ingress.
//
// The bot identity is required to filter our own posts out of the event stream;
// when it is not configured the ingress refuses to start rather than risk
// answering itself.
func NewIngress(params IngressParams) (*mattermost.Ingress, error) {
	var commands mattermost.IngressCommandSupport
	if params.Commands != nil {
		commands = params.Commands
	}
	return mattermost.NewIngress(mattermost.IngressParams{
		Processor:   params.Processor,
		Client:      params.Client,
		Commands:    commands,
		Enabled:     params.Enabled,
		BotUserID:   params.BotUserID,
		BotUsername: params.BotUsername,
		Logger:      params.Logger,
	}), nil
}
