package slackagentfx

import (
	"context"
	"net/url"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

type bindingServerParams struct {
	fx.In
	Processor   slackagent.InboundProcessor
	Canceller   slackagent.TurnCanceller
	Commands    commandcmd.Ingress
	Registry    *commandcmd.Registry
	Config      slackagent.Config
	Logger      zerolog.Logger
	Client      *slackagent.Client
	Invitations *auth.BindingInvitations `optional:"true"`
	Channels    *auth.BindingChannels    `optional:"true"`
}

func newBindingServer(p bindingServerParams) (*slackagent.Server, error) {
	server := slackagent.NewServer(p.Processor, p.Canceller, p.Commands, p.Registry, p.Config, p.Logger)
	if !p.Config.Enabled || p.Invitations == nil || p.Channels == nil {
		return server, nil
	}
	if err := p.Channels.RegisterResolver(slackagent.ChannelType, func(ctx context.Context) (usercmd.BindingChannel, error) {
		identity, err := p.Client.BindingIdentity(ctx)
		if err != nil {
			return usercmd.BindingChannel{}, err
		}
		return usercmd.BindingChannel{
			Integration:     usercmd.BindingIntegration{ChannelType: slackagent.ChannelType, Key: identity.TeamID + ":" + identity.UserID},
			Name:            identity.Team,
			BotUsername:     identity.User,
			Endpoint:        "slack://user?" + url.Values{"team": {identity.TeamID}, "id": {identity.UserID}}.Encode(),
			CommandsEnabled: true,
		}, nil
	}); err != nil {
		return nil, err
	}
	server.SetBindingAdmitter(&bindingAdmitter{invitations: p.Invitations, channels: p.Channels}, p.Client)
	return server, nil
}

type bindingAdmitter struct {
	invitations *auth.BindingInvitations
	channels    *auth.BindingChannels
}

func (a *bindingAdmitter) Consume(ctx context.Context, proof usercmd.BindingProof) (string, error) {
	info, ok := a.channels.Get(slackagent.ChannelType)
	if !ok {
		return "", usercmd.ErrBindingInvitationUnavailable
	}
	if info.Integration.Key == "" {
		var err error
		info, err = a.channels.Refresh(ctx, slackagent.ChannelType)
		if err != nil {
			return "", err
		}
	}
	// The signed sender's workspace must match the bot credential used to issue this invitation.
	identity := strings.Split(info.Integration.Key, ":")
	subject := strings.Split(proof.Principal, ":")
	if len(identity) != 2 || len(subject) != 3 || subject[0] != slackagent.ChannelType || subject[1] != identity[0] {
		return "", usercmd.ErrBindingInvitationScope
	}
	address, ok, err := slackagent.DecodeLocator(proof.Locator)
	if err != nil || !ok || address.TeamID != identity[0] {
		return "", usercmd.ErrBindingInvitationScope
	}
	proof.Integration = info.Integration
	proof.Principal = subject[1] + ":" + subject[2]
	if proof.DisplayName == "" {
		proof.DisplayName = subject[2]
	}
	return a.invitations.Consume(ctx, proof)
}
