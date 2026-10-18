package backoffice

import (
	"slices"
	"testing"
	"time"
)

func TestResolveServerConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   ServerConfig
		wantErr bool
	}{
		{name: "defaults", input: ServerConfig{}},
		{name: "monthly refresh", input: ServerConfig{RefreshTokenTTL: "720h"}},
		{name: "unbounded refresh", input: ServerConfig{RefreshTokenTTL: "721h"}, wantErr: true},
		{name: "nonpositive refresh", input: ServerConfig{RefreshTokenTTL: "0h"}, wantErr: true},
		{name: "refresh equals access", input: ServerConfig{RefreshTokenTTL: "15m"}, wantErr: true},
		{name: "bounded MFA ceremony", input: ServerConfig{CeremonyTTL: "15m"}},
		{name: "unbounded ceremony", input: ServerConfig{CeremonyTTL: "16m"}, wantErr: true},
		{name: "negative ceremony", input: ServerConfig{CeremonyTTL: "-1m"}, wantErr: true},
		{name: "https non-loopback", input: ServerConfig{ListenAddr: "0.0.0.0:8095", PublicURL: "https://admin.example.com", AccessTokenTTL: "10m", RefreshTokenTTL: "8h"}},
		{name: "canonical base path", input: ServerConfig{ListenAddr: "0.0.0.0:8095", PublicURL: "https://admin.example.com", BasePath: "/balda"}},
		{name: "trailing slash base path", input: ServerConfig{BasePath: "/balda/"}, wantErr: true},
		{name: "ambiguous base path", input: ServerConfig{BasePath: "/balda/../admin"}, wantErr: true},
		{name: "encoded base path", input: ServerConfig{BasePath: "/balda%2fadmin"}, wantErr: true},
		{name: "spaced base path", input: ServerConfig{BasePath: "/balda admin"}, wantErr: true},
		{name: "plain non-loopback", input: ServerConfig{ListenAddr: "0.0.0.0:8095", PublicURL: "http://admin.example.com", AccessTokenTTL: "10m", RefreshTokenTTL: "8h"}, wantErr: true},
		{name: "invalid listener port", input: ServerConfig{ListenAddr: "127.0.0.1:not-a-port"}, wantErr: true},
		{name: "inverted token lifetime", input: ServerConfig{AccessTokenTTL: "12h", RefreshTokenTTL: "12h"}, wantErr: true},
		{name: "unbounded access lifetime", input: ServerConfig{AccessTokenTTL: "2h", RefreshTokenTTL: "12h"}, wantErr: true},
		{name: "public URL credentials", input: ServerConfig{PublicURL: "https://user:secret@admin.example.com"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.input.Resolve()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Resolve() error = %v, wantErr %t", err, tt.wantErr)
			}
			if err == nil && (got.AccessTokenTTL <= 0 || got.AccessTokenTTL >= got.RefreshTokenTTL) {
				t.Fatalf("resolved token TTLs = %s/%s", got.AccessTokenTTL, got.RefreshTokenTTL)
			}
		})
	}
}

func TestProjectCapabilityCardsOmitsDisabledAndShowsOperationalSettings(t *testing.T) {
	t.Parallel()
	cfg := BaldaConfig{
		Telegram: TelegramConfig{Enabled: true, FormattingMode: "markdown", Webhook: TelegramWebhookConfig{Enabled: true, CallbackURL: "/gateway/telegram/webhook"}},
		Zulip:    ZulipConfig{Webhook: ZulipWebhookConfig{Enabled: false}},
		Slack: SlackConfig{
			Agent: SlackAgentConfig{Enabled: true, IncludePrivateChannels: false, EnableStreaming: true, SuggestedPrompts: false, EventsCallbackURL: "/gateway/slack/events", CommandsCallbackURL: "/gateway/slack/commands"},
		},
		Webhooks:   WebhooksConfig{Enabled: true, RouteCount: 1},
		Mattermost: MattermostConfig{Enabled: true, ServerURL: "https://mattermost.example", CommandsEnabled: true, CommandsCallbackURL: "/gateway/mattermost/commands"},
	}
	cards := ProjectCapabilityCards(cfg)
	if len(cards) != 4 {
		t.Fatalf("capability count = %d, want 4: %+v", len(cards), cards)
	}
	for _, card := range cards {
		if card.ID == "zulip" {
			t.Fatal("disabled Zulip capability was projected")
		}
		if card.ID == "webhooks" && card.RouteCount != 1 {
			t.Fatalf("webhook route count = %d, want 1", card.RouteCount)
		}
		if card.ID == "slackagent" {
			if len(card.Settings) != 3 || card.Settings[0].Value != "No" || card.Settings[1].Value != "Yes" || card.Settings[2].Value != "No" {
				t.Fatalf("Slack settings = %+v", card.Settings)
			}
			if len(card.Endpoints) != 2 || card.Endpoints[0].URL != "/gateway/slack/events" || card.Endpoints[1].URL != "/gateway/slack/commands" {
				t.Fatalf("Slack endpoints = %+v", card.Endpoints)
			}
		}
	}
	if got := configuredBindingChannels(cfg); !slices.Equal(got, []string{"telegram", "slackagent", "mattermost"}) {
		t.Fatalf("configured binding channels = %v", got)
	}
}

func TestDefaultResolvedTTLs(t *testing.T) {
	t.Parallel()
	resolved, err := (ServerConfig{}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AccessTokenTTL != 15*time.Minute || resolved.RefreshTokenTTL != 30*24*time.Hour || resolved.CeremonyTTL != 5*time.Minute {
		t.Fatalf("default TTLs = %s/%s/%s", resolved.AccessTokenTTL, resolved.RefreshTokenTTL, resolved.CeremonyTTL)
	}
}

func TestResolveExplicitRefreshTTL(t *testing.T) {
	t.Parallel()
	got, err := (ServerConfig{RefreshTokenTTL: "12h"}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshTokenTTL != 12*time.Hour {
		t.Fatalf("explicit refresh TTL = %s", got.RefreshTokenTTL)
	}
}
