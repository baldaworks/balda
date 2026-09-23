package backoffice

import (
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
		{name: "https non-loopback", input: ServerConfig{ListenAddr: "0.0.0.0:8095", PublicURL: "https://admin.example.com", AccessTokenTTL: "10m", RefreshTokenTTL: "8h"}},
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
		Webhooks: WebhooksConfig{Enabled: true, ListenAddr: "127.0.0.1:8093", RouteCount: 1},
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
}

func TestDefaultResolvedTTLs(t *testing.T) {
	t.Parallel()
	resolved, err := (ServerConfig{}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.AccessTokenTTL != 15*time.Minute || resolved.RefreshTokenTTL != 12*time.Hour {
		t.Fatalf("default TTLs = %s/%s", resolved.AccessTokenTTL, resolved.RefreshTokenTTL)
	}
}
