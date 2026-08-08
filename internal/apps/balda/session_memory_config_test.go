package balda

import (
	"context"
	"strings"
	"testing"
	"time"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	badgerstore "github.com/normahq/balda/sessionmemory/store/badger"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
)

func TestSessionMemoryConfigDisabledIgnoresOptionalValues(t *testing.T) {
	cfg := SessionMemoryConfig{
		Enabled:       false,
		Provider:      "missing-provider",
		MaxAge:        "not-a-duration",
		MaxBytes:      "not-bytes",
		MaxMsgSize:    "not-bytes",
		SearchTimeout: "not-a-duration",
		Retry:         SessionMemoryRetryConfig{BaseDelay: "not-a-duration"},
	}
	if err := validateSessionMemoryConfig(cfg); err != nil {
		t.Fatalf("validateSessionMemoryConfig() error = %v, want nil while disabled", err)
	}
	workerCfg, err := sessionMemoryWorkerConfig(cfg)
	if err != nil {
		t.Fatalf("sessionMemoryWorkerConfig() error = %v, want nil while disabled", err)
	}
	if workerCfg.Enabled {
		t.Fatal("disabled worker config unexpectedly enabled")
	}
}

func TestResolveSessionMemoryProviderIDPrefersDedicatedProvider(t *testing.T) {
	providers := map[string]agentconfig.Config{
		"chat":   sessionMemoryTestProviderConfig("chat-model"),
		"memory": sessionMemoryTestProviderConfig("memory-model"),
	}
	factory := agentfactory.New(providers, nil)

	providerID, err := validateSessionMemoryProviderConfig(
		SessionMemoryConfig{Enabled: true, Provider: " memory "},
		"chat",
		providers,
		factory,
	)
	if err != nil {
		t.Fatalf("validateSessionMemoryProviderConfig() error = %v", err)
	}
	if providerID != "memory" {
		t.Fatalf("selected provider = %q, want memory", providerID)
	}
}

func TestResolveSessionMemoryProviderIDFallsBackToBaldaProvider(t *testing.T) {
	providers := map[string]agentconfig.Config{
		"chat": sessionMemoryTestProviderConfig("chat-model"),
	}
	providerID, err := validateSessionMemoryProviderConfig(
		SessionMemoryConfig{Enabled: true},
		" chat ",
		providers,
		agentfactory.New(providers, nil),
	)
	if err != nil {
		t.Fatalf("validateSessionMemoryProviderConfig() error = %v", err)
	}
	if providerID != "chat" {
		t.Fatalf("fallback provider = %q, want chat", providerID)
	}
}

func TestValidateSessionMemoryProviderConfigRejectsMissingProvider(t *testing.T) {
	providers := map[string]agentconfig.Config{
		"chat": sessionMemoryTestProviderConfig("chat-model"),
	}
	_, err := validateSessionMemoryProviderConfig(
		SessionMemoryConfig{Enabled: true, Provider: "memory"},
		"chat",
		providers,
		agentfactory.New(providers, nil),
	)
	if err == nil || !strings.Contains(err.Error(), `balda.session_memory.provider "memory"`) {
		t.Fatalf("validateSessionMemoryProviderConfig() error = %v, want missing provider field", err)
	}
}

func TestValidateSessionMemoryProviderConfigRequiresProviderWhenEnabled(t *testing.T) {
	_, err := validateSessionMemoryProviderConfig(
		SessionMemoryConfig{Enabled: true},
		" ",
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "balda.session_memory.provider") {
		t.Fatalf("validateSessionMemoryProviderConfig() error = %v, want provider requirement", err)
	}
}

func TestValidateSessionMemoryProviderConfigDisabledIsInert(t *testing.T) {
	providerID, err := validateSessionMemoryProviderConfig(
		SessionMemoryConfig{Provider: "missing-provider"},
		" ",
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("validateSessionMemoryProviderConfig() error = %v, want nil while disabled", err)
	}
	if providerID != "" {
		t.Fatalf("disabled provider ID = %q, want empty", providerID)
	}
}

func TestValidateSessionMemoryProviderConfigRejectsInvalidProviderConfig(t *testing.T) {
	providers := map[string]agentconfig.Config{
		"memory": {
			Type:   "unsupported",
			OpenAI: &agentconfig.LocalAPIConfig{APIKey: "session-memory-secret", Model: "fast-model"},
		},
	}
	_, err := validateSessionMemoryProviderConfig(
		SessionMemoryConfig{Enabled: true, Provider: "memory"},
		"chat",
		providers,
		agentfactory.New(providers, nil),
	)
	if err == nil || !strings.Contains(err.Error(), "invalid provider configuration") {
		t.Fatalf("validateSessionMemoryProviderConfig() error = %v, want invalid provider configuration", err)
	}
	if strings.Contains(err.Error(), "session-memory-secret") {
		t.Fatalf("validateSessionMemoryProviderConfig() leaked provider secret: %v", err)
	}
}

func TestSessionMemoryProviderSettingsComeOnlyFromSelectedEntry(t *testing.T) {
	providers := map[string]agentconfig.Config{
		"chat": {
			Type:     agentconfig.AgentTypeCodexACP,
			CodexACP: &agentconfig.ACPConfig{Model: "chat-expensive", ReasoningEffort: "high"},
		},
		"memory": {
			Type:     agentconfig.AgentTypeCodexACP,
			CodexACP: &agentconfig.ACPConfig{Model: "memory-fast"},
		},
	}
	factory := agentfactory.New(providers, nil)
	providerID, err := validateSessionMemoryProviderConfig(
		SessionMemoryConfig{Enabled: true, Provider: "memory"},
		"chat",
		providers,
		factory,
	)
	if err != nil {
		t.Fatalf("validateSessionMemoryProviderConfig() error = %v", err)
	}
	resolved, err := agentconfig.NormalizeConfig(providers[providerID], "")
	if err != nil {
		t.Fatalf("agentconfig.NormalizeConfig() error = %v", err)
	}
	if resolved.Model != "memory-fast" {
		t.Fatalf("selected provider model = %q, want memory-fast", resolved.Model)
	}
	if resolved.ReasoningEffort != "" {
		t.Fatalf("selected provider reasoning = %q, want empty", resolved.ReasoningEffort)
	}

	providers["memory"].CodexACP.ReasoningEffort = "low"
	resolved, err = agentconfig.NormalizeConfig(providers["memory"], "")
	if err != nil {
		t.Fatalf("agentconfig.NormalizeConfig() with explicit reasoning error = %v", err)
	}
	if resolved.ReasoningEffort != "low" {
		t.Fatalf("selected provider reasoning = %q, want low", resolved.ReasoningEffort)
	}
}

func sessionMemoryTestProviderConfig(model string) agentconfig.Config {
	return agentconfig.Config{
		Type:     agentconfig.AgentTypeCodexACP,
		CodexACP: &agentconfig.ACPConfig{Model: model},
	}
}

func TestCanonicalSessionMemoryRuntimeComposesPortableCapabilities(t *testing.T) {
	stateDir := t.TempDir()
	runtime, err := newCanonicalSessionMemoryRuntime(SessionMemoryConfig{Enabled: true}, &baldaagent.Builder{}, "provider", t.TempDir(), stateDir)
	if err != nil {
		t.Fatalf("newCanonicalSessionMemoryRuntime() error = %v", err)
	}
	if runtime == nil {
		t.Fatal("newCanonicalSessionMemoryRuntime() returned nil runtime")
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("portable runtime Start() error = %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("portable runtime Close() error = %v", err)
	}
	store, err := badgerstore.OpenBadgerSessionMemoryStore(baldastate.SessionMemoryCanonicalPath(stateDir))
	if err != nil {
		t.Fatalf("canonical store did not reopen after portable runtime close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("reopened canonical store Close() error = %v", err)
	}
}

func TestSessionMemoryConfigValidationRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  SessionMemoryConfig
		want string
	}{
		{name: "bad retention", cfg: SessionMemoryConfig{Enabled: true, MaxBytes: "zero"}, want: "max_bytes"},
		{name: "bad derivation timeout", cfg: SessionMemoryConfig{Enabled: true, Derivation: SessionMemoryDerivationConfig{Timeout: "bad"}}, want: "derivation.timeout"},
		{name: "bad stream name", cfg: SessionMemoryConfig{Enabled: true, Stream: "memory stream"}, want: "stream"},
		{name: "bad trusted tool", cfg: SessionMemoryConfig{Enabled: true, TrustedTools: []string{"calendar lookup"}}, want: "trusted_tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSessionMemoryConfig(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateSessionMemoryConfig() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSessionMemoryExecutionConfigMapsDurabilitySettings(t *testing.T) {
	public := SessionMemoryConfig{
		Enabled:         true,
		Stream:          "MEMORY",
		Consumer:        "MEMORY_WORKER",
		AckWait:         "4m",
		FetchWait:       "2s",
		PublishTimeout:  "3s",
		PublishAttempts: 4,
		MaxAge:          "14d",
		MaxBytes:        "32MiB",
		MaxMsgSize:      "2MiB",
	}
	got := sessionMemoryExecutionConfig(public)
	want := baldaexecution.SessionMemoryConfig{
		Enabled:         true,
		Stream:          "MEMORY",
		Consumer:        "MEMORY_WORKER",
		AckWait:         "4m",
		FetchWait:       "2s",
		PublishTimeout:  "3s",
		PublishAttempts: 4,
		MaxAge:          "14d",
		MaxBytes:        "32MiB",
		MaxMsgSize:      "2MiB",
	}
	if got != want {
		t.Fatalf("sessionMemoryExecutionConfig() = %+v, want %+v", got, want)
	}
}

func TestSessionMemoryWorkerConfigUsesApplicationRetryPort(t *testing.T) {
	cfg := SessionMemoryConfig{
		Enabled: true,
		Retry: SessionMemoryRetryConfig{
			MaxAttempts:      7,
			BaseDelay:        "100ms",
			MaxDelay:         "1s",
			ProgressInterval: "2s",
			FetchErrorDelay:  "50ms",
			ShutdownTimeout:  "10s",
		},
		MaxConcurrentScopes: 6,
		MaxQueuedPerScope:   17,
	}
	got, err := sessionMemoryWorkerConfig(cfg)
	if err != nil {
		t.Fatalf("sessionMemoryWorkerConfig() error = %v", err)
	}
	want := sessionmemoryapp.Config{Enabled: true, MaxAttempts: 7}
	if got.Enabled != want.Enabled || got.MaxAttempts != want.MaxAttempts || got.RetryBaseDelay.String() != "100ms" || got.RetryMaxDelay.String() != "1s" || got.ProgressInterval.String() != "2s" || got.FetchErrorDelay.String() != "50ms" || got.ShutdownTimeout.String() != "10s" || got.MaxConcurrentScopes != 6 || got.MaxQueuedPerScope != 17 {
		t.Fatalf("worker config = %+v, want configured retry values", got)
	}
}

func TestSessionMemoryIngressOutboxConfigUsesBoundedRetry(t *testing.T) {
	cfg := SessionMemoryConfig{
		Enabled: true,
		Retry:   SessionMemoryRetryConfig{MaxAttempts: 7, BaseDelay: "100ms", MaxDelay: "1s"},
	}
	got, err := sessionMemoryIngressOutboxConfig(cfg)
	if err != nil {
		t.Fatalf("sessionMemoryIngressOutboxConfig() error = %v", err)
	}
	if !got.Enabled || got.MaxAttempts != 7 || got.RetryBaseDelay != 100*time.Millisecond || got.RetryMaxDelay != time.Second {
		t.Fatalf("ingress config = %+v, want configured retry values", got)
	}
}
