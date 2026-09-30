package zulipfx

import (
	"context"
	"strconv"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"go.uber.org/fx"
)

type bindingIdentityParams struct {
	fx.In
	Client   *zulip.Client
	Channels *auth.BindingChannels `optional:"true"`
	Enabled  bool                  `name:"balda_zulip_webhook_enabled"`
}

func registerBindingIdentity(p bindingIdentityParams) error {
	if !p.Enabled || p.Channels == nil {
		return nil
	}
	return p.Channels.RegisterResolver(zulip.ChannelType, func(ctx context.Context) (usercmd.BindingChannel, error) {
		identity, err := p.Client.BindingIdentity(ctx)
		if err != nil {
			return usercmd.BindingChannel{}, err
		}
		id := strconv.Itoa(identity.UserID)
		return usercmd.BindingChannel{
			Integration: usercmd.BindingIntegration{ChannelType: zulip.ChannelType, Key: identity.RealmURL + "#" + id},
			Name:        identity.FullName, BotUsername: identity.Email,
			Endpoint:        identity.RealmURL + "/#narrow/dm/" + id,
			CommandsEnabled: true,
		}, nil
	})
}
