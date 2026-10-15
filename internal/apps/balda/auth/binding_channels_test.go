package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestBindingChannelsIdentityRecovery(t *testing.T) {
	t.Parallel()
	registry := NewBindingChannels([]string{ChannelTelegram, ChannelSlack})
	telegram := usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: ChannelTelegram, Key: "123"}, BotUsername: "verified_bot"}
	if err := registry.Register(telegram); err != nil {
		t.Fatal(err)
	}
	if got, err := registry.Refresh(t.Context(), ChannelTelegram); err != nil || got.BotUsername != telegram.BotUsername {
		t.Fatalf("started Telegram identity = %+v: %v", got, err)
	}
	slack := usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: ChannelSlack, Key: "T1:U1"}}
	unavailable := errors.New("provider unavailable")
	resolveError := error(nil)
	if err := registry.RegisterResolver(ChannelSlack, func(context.Context) (usercmd.BindingChannel, error) { return slack, resolveError }); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Refresh(t.Context(), ChannelSlack); err != nil {
		t.Fatal(err)
	}
	resolveError = unavailable
	if _, err := registry.Refresh(t.Context(), ChannelSlack); !errors.Is(err, unavailable) {
		t.Fatalf("failed lookup = %v", err)
	}
	if info, _ := registry.Get(ChannelSlack); info.Integration.Key != "" {
		t.Fatal("failed identity lookup left issuance available")
	}
	resolveError = nil
	slack.Integration.ChannelType = ChannelTelegram
	if _, err := registry.Refresh(t.Context(), ChannelSlack); !errors.Is(err, usercmd.ErrBindingInvitationScope) {
		t.Fatalf("wrong adapter scope = %v", err)
	}
	slack.Integration.ChannelType = ChannelSlack
	if got, err := registry.Refresh(t.Context(), ChannelSlack); err != nil || got.Integration.Key != "T1:U1" {
		t.Fatalf("identity recovery = %+v: %v", got, err)
	}
}
