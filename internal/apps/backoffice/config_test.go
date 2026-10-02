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
		{name: "bounded MFA lifetimes", input: ServerConfig{CeremonyTTL: "15m", StepUpTTL: "1h"}},
		{name: "unbounded ceremony", input: ServerConfig{CeremonyTTL: "16m"}, wantErr: true},
		{name: "unbounded step-up", input: ServerConfig{StepUpTTL: "2h"}, wantErr: true},
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

func TestProjectConfiguredCapabilitiesOmitsDisabled(t *testing.T) {
	t.Parallel()
	cfg := BaldaConfig{
		Telegram: TelegramConfig{Enabled: true, Webhook: TelegramWebhookConfig{Enabled: true, ListenAddr: "127.0.0.1:8080", Path: "/telegram"}},
		Zulip:    ZulipConfig{Webhook: ZulipWebhookConfig{Enabled: false}},
		Slack: SlackConfig{
			Enabled: true, ListenAddr: "127.0.0.1:8091", EventsPath: "/slack/events",
			Agent: SlackAgentConfig{Enabled: true, ListenAddr: "127.0.0.1:8092", EventsPath: "/slack/agent/events", EnableStreaming: true},
		},
		Webhooks:   WebhooksConfig{Enabled: true, ListenAddr: "127.0.0.1:8093", RouteCount: 1},
		Mattermost: MattermostConfig{Enabled: true, CommandsEnabled: true, ListenAddr: "127.0.0.1:8094", CommandsPath: "/mattermost/commands"},
	}
	projection := ProjectConfiguredCapabilities(cfg)
	if len(projection) != 4 {
		t.Fatalf("capability count = %d, want 4: %+v", len(projection), projection)
	}
	for _, capability := range projection {
		if capability.ID == "zulip" {
			t.Fatal("disabled Zulip capability was projected")
		}
		if capability.ID == "webhooks" && capability.RouteCount != 1 {
			t.Fatalf("webhook route count = %d, want 1", capability.RouteCount)
		}
	}
	cards := ProjectCapabilityCards(cfg)
	if len(cards) != len(projection) || cards[0].ID != projection[0].ID {
		t.Fatalf("capability cards = %+v", cards)
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
	if resolved.AccessTokenTTL != 15*time.Minute || resolved.RefreshTokenTTL != 30*24*time.Hour || resolved.CeremonyTTL != 5*time.Minute || resolved.StepUpTTL != 15*time.Minute {
		t.Fatalf("default TTLs = %s/%s", resolved.AccessTokenTTL, resolved.RefreshTokenTTL)
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
