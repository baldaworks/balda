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

func TestProjectSystemSnapshotContainsOnlyExplicitConfiguredFields(t *testing.T) {
	t.Parallel()
	snapshot := ProjectSystemSnapshot(ResolvedConfig{
		Agent: AgentConfig{ProviderID: "primary", ProviderType: "openai", Profile: "default", Model: "gpt-test", ReasoningEffort: "high", GlobalInstruction: "be useful"},
		Balda: BaldaConfig{Telegram: TelegramConfig{Enabled: true, FormattingMode: "markdown"}},
	})
	if len(snapshot.Capabilities) != 1 || snapshot.Capabilities[0].ID != "telegram" {
		t.Fatalf("capabilities = %+v", snapshot.Capabilities)
	}
	if snapshot.Agent.ProviderID != "primary" || snapshot.Agent.Model != "gpt-test" || snapshot.Agent.GlobalInstruction != "be useful" {
		t.Fatalf("agent = %+v", snapshot.Agent)
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
