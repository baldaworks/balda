// Package startupnotifyfx adapts enabled channel transports to startup notification ports.
package startupnotifyfx

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/startupnotifyapp"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"go.uber.org/fx"
)

// Module contributes transport-specific direct-message resolvers to the startup notifier.
var Module = fx.Module("balda_startup_notify_fx",
	fx.Provide(
		fx.Annotate(newTelegramResolverBinding,
			fx.ParamTags(`name:"balda_telegram_enabled"`),
			fx.ResultTags(`group:"balda_startup_dm_resolver"`)),
		fx.Annotate(newZulipResolverBinding,
			fx.ParamTags(`name:"balda_zulip_webhook_enabled"`),
			fx.ResultTags(`group:"balda_startup_dm_resolver"`)),
		fx.Annotate(newSlackResolverBinding,
			fx.ResultTags(`group:"balda_startup_dm_resolver"`)),
		fx.Annotate(newMattermostResolverBinding,
			fx.ParamTags(``, `name:"balda_mattermost_enabled"`, `name:"balda_mattermost_bot_user_id"`),
			fx.ResultTags(`group:"balda_startup_dm_resolver"`)),
	),
)

type telegramResolver struct{}

func (telegramResolver) ResolveDM(_ context.Context, binding usercmd.Binding) (deliverycmd.Locator, error) {
	principal, err := positiveInt64(binding.Principal, "telegram principal")
	if err != nil {
		return deliverycmd.Locator{}, err
	}
	chatID, found, err := provenanceInt64(binding.Provenance, "chat_id")
	if err != nil {
		return deliverycmd.Locator{}, err
	}
	if !found {
		chatID = principal
	}
	if chatID <= 0 {
		return deliverycmd.Locator{}, fmt.Errorf("telegram chat_id must be positive")
	}
	return telegram.NewLocator(chatID, 0), nil
}

func newTelegramResolverBinding(enabled bool) startupnotifyapp.ResolverBinding {
	return startupnotifyapp.ResolverBinding{ChannelType: telegram.ChannelType, Enabled: enabled, Resolver: telegramResolver{}}
}

type zulipResolver struct{}

func (zulipResolver) ResolveDM(_ context.Context, binding usercmd.Binding) (deliverycmd.Locator, error) {
	principal, err := strconv.Atoi(strings.TrimSpace(binding.Principal))
	if err != nil || principal <= 0 {
		return deliverycmd.Locator{}, fmt.Errorf("zulip principal must be a positive integer")
	}
	return zulip.NewDMLocator(principal), nil
}

func newZulipResolverBinding(enabled bool) startupnotifyapp.ResolverBinding {
	return startupnotifyapp.ResolverBinding{ChannelType: zulip.ChannelType, Enabled: enabled, Resolver: zulipResolver{}}
}

type slackConversationOpener interface {
	OpenConversation(ctx context.Context, userID string) (string, error)
}

type slackResolver struct {
	client slackConversationOpener
}

func (r slackResolver) ResolveDM(ctx context.Context, binding usercmd.Binding) (deliverycmd.Locator, error) {
	teamID, userID, ok := strings.Cut(strings.TrimSpace(binding.Principal), ":")
	teamID, userID = strings.TrimSpace(teamID), strings.TrimSpace(userID)
	if !ok || teamID == "" || userID == "" || strings.Contains(userID, ":") {
		return deliverycmd.Locator{}, fmt.Errorf("slack principal must be <team_id>:<user_id>")
	}
	conversationID, err := r.client.OpenConversation(ctx, userID)
	if err != nil {
		return deliverycmd.Locator{}, fmt.Errorf("open slack direct conversation: %w", err)
	}
	locator := slackagent.NewConversationLocator(teamID, conversationID)
	if scope, err := slackagent.ClassifyLocatorScope(locator); err != nil || scope != deliverycmd.LocatorScopePersonal {
		return deliverycmd.Locator{}, fmt.Errorf("slack conversations.open returned a non-DM conversation")
	}
	return locator, nil
}

func newSlackResolverBinding(cfg slackagent.Config, client *slackagent.Client) startupnotifyapp.ResolverBinding {
	return startupnotifyapp.ResolverBinding{ChannelType: slackagent.ChannelType, Enabled: cfg.Enabled, Resolver: slackResolver{client: client}}
}

type mattermostDirectChannelClient interface {
	GetDirectChannel(ctx context.Context, botUserID, otherUserID string) (mattermost.Channel, error)
}

type mattermostResolver struct {
	client    mattermostDirectChannelClient
	botUserID string
}

func (r mattermostResolver) ResolveDM(ctx context.Context, binding usercmd.Binding) (deliverycmd.Locator, error) {
	principal := strings.TrimSpace(binding.Principal)
	if principal == "" {
		return deliverycmd.Locator{}, fmt.Errorf("mattermost principal is required")
	}
	channel, err := r.client.GetDirectChannel(ctx, r.botUserID, principal)
	if err != nil {
		return deliverycmd.Locator{}, fmt.Errorf("resolve mattermost direct channel: %w", err)
	}
	if strings.TrimSpace(channel.ID) == "" || !strings.EqualFold(strings.TrimSpace(channel.Type), "D") {
		return deliverycmd.Locator{}, fmt.Errorf("mattermost direct-channel response is invalid")
	}
	return mattermost.NewDMLocator(channel.ID, principal), nil
}

func newMattermostResolverBinding(client *mattermost.Client, enabled bool, botUserID string) startupnotifyapp.ResolverBinding {
	return startupnotifyapp.ResolverBinding{
		ChannelType: mattermost.ChannelType,
		Enabled:     enabled,
		Resolver:    mattermostResolver{client: client, botUserID: strings.TrimSpace(botUserID)},
	}
}

func positiveInt64(value, field string) (int64, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}
	return parsed, nil
}

func provenanceInt64(provenance, key string) (int64, bool, error) {
	prefix := strings.TrimSpace(key) + "="
	for _, field := range strings.Split(provenance, ";") {
		value, ok := strings.CutPrefix(strings.TrimSpace(field), prefix)
		if !ok {
			continue
		}
		parsed, err := positiveInt64(value, "telegram "+key)
		if err != nil {
			return 0, true, err
		}
		return parsed, true, nil
	}
	return 0, false, nil
}
