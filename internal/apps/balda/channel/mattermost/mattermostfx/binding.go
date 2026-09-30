package mattermostfx

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"go.uber.org/fx"
)

type bindingIdentityParams struct {
	fx.In
	Client          *mattermost.Client
	Channels        *auth.BindingChannels `optional:"true"`
	Enabled         bool                  `name:"balda_mattermost_enabled"`
	ServerURL       string                `name:"balda_mattermost_server_url"`
	BotUserID       string                `name:"balda_mattermost_bot_user_id"`
	BotUsername     string                `name:"balda_mattermost_bot_username"`
	CommandsEnabled bool                  `name:"balda_mattermost_commands_enabled"`
}

func registerBindingIdentity(p bindingIdentityParams) error {
	if !p.Enabled || p.Channels == nil {
		return nil
	}
	return p.Channels.RegisterResolver(mattermost.ChannelType, func(ctx context.Context) (usercmd.BindingChannel, error) {
		user, err := p.Client.GetMe(ctx)
		if err != nil {
			return usercmd.BindingChannel{}, err
		}
		if user.ID != strings.TrimSpace(p.BotUserID) || !strings.EqualFold(user.Username, strings.TrimSpace(p.BotUsername)) || user.DeleteAt != 0 {
			return usercmd.BindingChannel{}, usercmd.ErrBindingInvitationScope
		}
		endpoint := strings.TrimRight(strings.TrimSpace(p.ServerURL), "/")
		return usercmd.BindingChannel{
			Integration: usercmd.BindingIntegration{ChannelType: mattermost.ChannelType, Key: endpoint + "#" + user.ID},
			Name:        user.Nickname, BotUsername: user.Username, Endpoint: endpoint, CommandsEnabled: p.CommandsEnabled,
		}, nil
	})
}
