package balda

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/sessionmemory/sessionmemorytest"
)

func TestSessionMemoryConfigDisabledIgnoresOptionalValues(t *testing.T) {
	cfg := SessionMemoryConfig{
		Enabled:       false,
		MaxAge:        "not-a-duration",
		MaxBytes:      "not-bytes",
		MaxMsgSize:    "not-bytes",
		SearchTimeout: "not-a-duration",
		Retry:         SessionMemoryRetryConfig{BaseDelay: "not-a-duration"},
	}
	if err := validateSessionMemoryConfig(cfg); err != nil {
		t.Fatalf("validateSessionMemoryConfig() error = %v, want nil while disabled", err)
	}
	if _, err := newSessionMemoryProvider(cfg, nil, nil, "", ""); err != nil {
		t.Fatalf("newSessionMemoryProvider() error = %v, want nil while disabled", err)
	}
	workerCfg, err := sessionMemoryWorkerConfig(cfg)
	if err != nil {
		t.Fatalf("sessionMemoryWorkerConfig() error = %v, want nil while disabled", err)
	}
	if workerCfg.Enabled {
		t.Fatal("disabled worker config unexpectedly enabled")
	}
}

func TestCanonicalSessionMemoryProviderDisabledDoesNotOpenCanonicalStore(t *testing.T) {
	stateDir := t.TempDir()
	provider, err := newCanonicalSessionMemoryProvider(SessionMemoryConfig{}, nil, nil, "", "", stateDir)
	if err != nil {
		t.Fatalf("newCanonicalSessionMemoryProvider() error = %v", err)
	}
	if _, ok := provider.(sessionmemoryapp.DisabledProvider); !ok {
		t.Fatalf("disabled provider type = %T, want sessionmemoryapp.DisabledProvider", provider)
	}
	if _, err := os.Stat(baldastate.SessionMemoryCanonicalPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("canonical path stat error = %v, want unopened path", err)
	}
}

func TestCanonicalSessionMemoryProviderOwnsBadgerLifecycle(t *testing.T) {
	stateDir := t.TempDir()
	provider, err := newCanonicalSessionMemoryProvider(SessionMemoryConfig{Enabled: true}, sessionmemorytest.NewStore(), &baldaagent.Builder{}, "provider", t.TempDir(), stateDir)
	if err != nil {
		t.Fatalf("newCanonicalSessionMemoryProvider() error = %v", err)
	}
	canonical, ok := provider.(*sessionmemoryapp.CanonicalProvider)
	if !ok {
		t.Fatalf("enabled provider type = %T, want *sessionmemoryapp.CanonicalProvider", provider)
	}
	if _, err := baldastate.OpenBadgerSessionMemoryStore(baldastate.SessionMemoryCanonicalPath(stateDir)); err == nil {
		t.Fatal("second canonical Badger owner opened while composition owner was active")
	}
	if err := canonical.Start(context.Background()); err != nil {
		t.Fatalf("canonical Start() error = %v", err)
	}
	if err := canonical.Close(context.Background()); err != nil {
		t.Fatalf("canonical Close() error = %v", err)
	}
	reopened, err := baldastate.OpenBadgerSessionMemoryStore(baldastate.SessionMemoryCanonicalPath(stateDir))
	if err != nil {
		t.Fatalf("canonical store did not reopen after OnStop(): %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("reopened canonical store Close() error = %v", err)
	}
}

func TestSessionMemoryConfigEnabledUsesNativeProvider(t *testing.T) {
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
