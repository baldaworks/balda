package balda

import (
	"strings"
	"testing"
	"time"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
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

func TestSessionMemoryConfigEnabledUsesNativeProvider(t *testing.T) {
	tests := []struct {
		name string
		cfg  SessionMemoryConfig
		want string
	}{
		{name: "bad retention", cfg: SessionMemoryConfig{Enabled: true, MaxBytes: "zero"}, want: "max_bytes"},
		{name: "bad derivation timeout", cfg: SessionMemoryConfig{Enabled: true, Derivation: SessionMemoryDerivationConfig{Timeout: "bad"}}, want: "derivation.timeout"},
		{name: "bad stream name", cfg: SessionMemoryConfig{Enabled: true, Stream: "memory stream"}, want: "stream"},
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
	}
	got, err := sessionMemoryWorkerConfig(cfg)
	if err != nil {
		t.Fatalf("sessionMemoryWorkerConfig() error = %v", err)
	}
	want := sessionmemoryapp.Config{Enabled: true, MaxAttempts: 7}
	if got.Enabled != want.Enabled || got.MaxAttempts != want.MaxAttempts || got.RetryBaseDelay.String() != "100ms" || got.RetryMaxDelay.String() != "1s" || got.ProgressInterval.String() != "2s" || got.FetchErrorDelay.String() != "50ms" || got.ShutdownTimeout.String() != "10s" {
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
