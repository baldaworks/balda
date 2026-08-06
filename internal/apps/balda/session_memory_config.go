package balda

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/sessionmemory"
)

const (
	defaultSessionMemorySearchTimeout = 5 * time.Second
)

type scrubbers struct {
	canonical interface {
		ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs, revisionIDs []string) error
	}
	projection interface {
		ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs, revisionIDs []string) error
	}
}

func (s scrubbers) ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs, revisionIDs []string) error {
	// Canonical denial is committed before this hook.  Keep payload and
	// projection cleanup independent so a disposable index failure cannot make
	// an already-forgotten record visible again.
	return errors.Join(
		s.canonical.ScrubCanonicalForget(ctx, scope, sourceIDs, revisionIDs),
		s.projection.ScrubCanonicalForget(ctx, scope, sourceIDs, revisionIDs),
	)
}

// sessionMemoryExecutionConfig maps the public balda.session_memory block to
// the runtime-owned JetStream settings.
func sessionMemoryExecutionConfig(cfg SessionMemoryConfig) baldaexecution.SessionMemoryConfig {
	return baldaexecution.SessionMemoryConfig{
		Enabled:         cfg.Enabled,
		Stream:          strings.TrimSpace(cfg.Stream),
		Consumer:        strings.TrimSpace(cfg.Consumer),
		AckWait:         strings.TrimSpace(cfg.AckWait),
		FetchWait:       strings.TrimSpace(cfg.FetchWait),
		PublishTimeout:  strings.TrimSpace(cfg.PublishTimeout),
		PublishAttempts: cfg.PublishAttempts,
		MaxAge:          strings.TrimSpace(cfg.MaxAge),
		MaxBytes:        strings.TrimSpace(cfg.MaxBytes),
		MaxMsgSize:      strings.TrimSpace(cfg.MaxMsgSize),
	}
}

func validateSessionMemoryConfig(cfg SessionMemoryConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if _, err := sessionmemoryapp.NewTrustedToolPolicy(cfg.TrustedTools); err != nil {
		return fmt.Errorf("balda.session_memory.trusted_tools: %w", err)
	}

	if cfg.Derivation.MaxOutputBytes < 0 {
		return fmt.Errorf("balda.session_memory.derivation.max_output_bytes must be non-negative")
	}
	for field, raw := range map[string]string{
		"balda.session_memory.ack_wait":        cfg.AckWait,
		"balda.session_memory.fetch_wait":      cfg.FetchWait,
		"balda.session_memory.publish_timeout": cfg.PublishTimeout,
		"balda.session_memory.max_age":         cfg.MaxAge,
	} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		value, err := parseSessionMemoryDuration(raw)
		if err != nil || value <= 0 {
			if err == nil {
				err = fmt.Errorf("must be greater than zero")
			}
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	if cfg.PublishAttempts < 0 {
		return fmt.Errorf("balda.session_memory.publish_attempts must be non-negative")
	}
	if cfg.Retry.MaxAttempts < 0 {
		return fmt.Errorf("balda.session_memory.retry.max_attempts must be non-negative")
	}
	if cfg.MaxConcurrentScopes < 0 || cfg.MaxConcurrentScopes > 128 {
		return fmt.Errorf("balda.session_memory.max_concurrent_scopes must be between 0 and 128")
	}
	if cfg.MaxQueuedPerScope < 0 || cfg.MaxQueuedPerScope > 1024 {
		return fmt.Errorf("balda.session_memory.max_queued_per_scope must be between 0 and 1024")
	}
	for field, raw := range map[string]string{
		"balda.session_memory.stream":   cfg.Stream,
		"balda.session_memory.consumer": cfg.Consumer,
	} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if err := validateIdentifierValue(field, raw); err != nil {
			return err
		}
	}
	for field, raw := range map[string]string{
		"balda.session_memory.retry.base_delay":        cfg.Retry.BaseDelay,
		"balda.session_memory.retry.max_delay":         cfg.Retry.MaxDelay,
		"balda.session_memory.retry.progress_interval": cfg.Retry.ProgressInterval,
		"balda.session_memory.retry.fetch_error_delay": cfg.Retry.FetchErrorDelay,
		"balda.session_memory.retry.shutdown_timeout":  cfg.Retry.ShutdownTimeout,
		"balda.session_memory.search_timeout":          cfg.SearchTimeout,
		"balda.session_memory.derivation.timeout":      cfg.Derivation.Timeout,
	} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		value, err := parseSessionMemoryDuration(raw)
		if err != nil || value <= 0 {
			if err == nil {
				err = fmt.Errorf("must be greater than zero")
			}
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	if base, maxDelayRaw := strings.TrimSpace(cfg.Retry.BaseDelay), strings.TrimSpace(cfg.Retry.MaxDelay); base != "" && maxDelayRaw != "" {
		baseDelay, _ := parseSessionMemoryDuration(base)
		maxDelay, _ := parseSessionMemoryDuration(maxDelayRaw)
		if maxDelay < baseDelay {
			return fmt.Errorf("balda.session_memory.retry.max_delay must be at least retry.base_delay")
		}
	}
	if raw := strings.TrimSpace(cfg.MaxBytes); raw != "" {
		if _, err := parseSessionMemoryLimit(raw, true); err != nil {
			return fmt.Errorf("balda.session_memory.max_bytes: %w", err)
		}
	}
	if raw := strings.TrimSpace(cfg.MaxMsgSize); raw != "" {
		value, err := parseSessionMemoryLimit(raw, true)
		if err != nil {
			return fmt.Errorf("balda.session_memory.max_msg_size: %w", err)
		}
		if value != -1 && value > math.MaxInt32 {
			return fmt.Errorf("balda.session_memory.max_msg_size exceeds int32 maximum")
		}
	}
	return nil
}

func newSessionMemoryProvider(cfg SessionMemoryConfig, store sessionmemory.Store, builder *baldaagent.Builder, providerID, workingDir string) (sessionmemory.Provider, error) {
	if !cfg.Enabled {
		return sessionmemoryapp.DisabledProvider{}, nil
	}
	provider, _, err := newNativeSessionMemoryComponents(cfg, store, builder, providerID, workingDir)
	return provider, err
}

func newSessionMemoryDeriver(cfg SessionMemoryConfig, builder *baldaagent.Builder, providerID, workingDir string) (*sessionmemoryapp.Deriver, sessionmemoryapp.StructuredInvoker, error) {
	if err := validateSessionMemoryConfig(cfg); err != nil {
		return nil, nil, err
	}
	derivationTimeout, err := optionalSessionMemoryDuration(cfg.Derivation.Timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("balda.session_memory.derivation.timeout: %w", err)
	}
	invoker, err := sessionmemoryapp.NewNormaInvoker(sessionmemoryapp.NormaInvokerConfig{
		Builder:    builder,
		ProviderID: providerID,
		WorkingDir: workingDir,
		MaxBytes:   cfg.Derivation.MaxOutputBytes,
		Timeout:    derivationTimeout,
	})
	if err != nil {
		return nil, nil, err
	}
	deriver, err := sessionmemoryapp.NewDeriver(invoker)
	if err != nil {
		_ = invoker.Close(context.Background())
		return nil, nil, err
	}
	return deriver, invoker, nil
}

func newNativeSessionMemoryComponents(cfg SessionMemoryConfig, store sessionmemory.Store, builder *baldaagent.Builder, providerID, workingDir string) (*sessionmemoryapp.NativeProvider, *sessionmemoryapp.Deriver, error) {
	deriver, invoker, err := newSessionMemoryDeriver(cfg, builder, providerID, workingDir)
	if err != nil {
		return nil, nil, err
	}
	provider, err := sessionmemoryapp.NewNativeProvider(store, deriver, invoker)
	if err != nil {
		_ = invoker.Close(context.Background())
		return nil, nil, err
	}
	return provider, deriver, nil
}

func newCanonicalSessionMemoryProvider(cfg SessionMemoryConfig, legacyStore sessionmemory.Store, builder *baldaagent.Builder, providerID, workingDir, stateDir string) (sessionmemory.Provider, error) {
	if !cfg.Enabled {
		return sessionmemoryapp.DisabledProvider{}, nil
	}
	deriver, invoker, err := newSessionMemoryDeriver(cfg, builder, providerID, workingDir)
	if err != nil {
		return nil, err
	}
	canonicalStore, err := baldastate.OpenBadgerSessionMemoryStore(baldastate.SessionMemoryCanonicalPath(stateDir))
	if err != nil {
		_ = invoker.Close(context.Background())
		return nil, fmt.Errorf("open canonical session-memory store: %w", err)
	}
	reader, err := baldastate.NewCanonicalReader(canonicalStore)
	if err != nil {
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	processor, err := sessionmemory.NewCanonicalTurnProcessor(canonicalStore, deriver, sessionmemory.PolicyRegistry{Version: "policy-v1"})
	if err != nil {
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	boundary, err := sessionmemoryapp.NewCanonicalBoundaryProcessor(canonicalStore, reader, deriver, deriver, sessionmemory.LegacyDerivationRef())
	if err != nil {
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	projection, err := baldastate.NewBleveRecallProjection(baldastate.SessionMemoryProjectionPath(stateDir))
	if err != nil {
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	applier, err := baldastate.NewBleveCanonicalApplier(projection, reader)
	if err != nil {
		_ = projection.Close()
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	projectionRuntime, err := sessionmemoryapp.NewProjectionRuntime(canonicalStore, canonicalStore, applier, "bleve", 0)
	if err != nil {
		_ = applier.Close()
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	recall, err := sessionmemoryapp.NewRecallService(reader, projection)
	if err != nil {
		_ = applier.Close()
		projectionRuntime.Close()
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	migration, err := sessionmemoryapp.NewMigrationCoordinator(legacyStore, canonicalStore, canonicalStore)
	if err != nil {
		_ = applier.Close()
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	forget, err := sessionmemoryapp.NewCanonicalForgetService(canonicalStore, canonicalStore, canonicalStore, canonicalStore, scrubbers{canonical: canonicalStore, projection: applier})
	if err != nil {
		_ = applier.Close()
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	maintenance, err := baldastate.NewCanonicalMaintenance(canonicalStore, baldastate.CanonicalMaintenanceConfig{})
	if err != nil {
		_ = applier.Close()
		_ = canonicalStore.Close()
		_ = invoker.Close(context.Background())
		return nil, err
	}
	startRuntime := func(ctx context.Context) error {
		if err := maintenance.Start(ctx); err != nil {
			_ = maintenance.Stop(context.Background())
			return err
		}
		return nil
	}
	closeRuntime := func(ctx context.Context) error {
		projectionRuntime.Close()
		return errors.Join(maintenance.Stop(ctx), applier.Close(), invoker.Close(ctx), canonicalStore.Close())
	}
	canonical, err := sessionmemoryapp.NewCanonicalProviderWithRuntime(sessionmemoryapp.CanonicalProviderConfig{
		Processor: processor, Boundary: boundary, Derived: reader, Recall: recall, Forget: forget,
		Before: migration.MigrateScope, Project: projectionRuntime.Sync, Start: startRuntime, Close: closeRuntime, Derivation: sessionmemory.LegacyDerivationRef(),
	})
	if err != nil {
		_ = closeRuntime(context.Background())
		return nil, err
	}
	return canonical, nil
}

func sessionMemoryWorkerConfig(cfg SessionMemoryConfig) (sessionmemoryapp.Config, error) {
	if err := validateSessionMemoryConfig(cfg); err != nil {
		return sessionmemoryapp.Config{}, err
	}
	if !cfg.Enabled {
		return sessionmemoryapp.Config{}, nil
	}
	baseDelay, err := optionalSessionMemoryDuration(cfg.Retry.BaseDelay)
	if err != nil {
		return sessionmemoryapp.Config{}, fmt.Errorf("balda.session_memory.retry.base_delay: %w", err)
	}
	maxDelay, err := optionalSessionMemoryDuration(cfg.Retry.MaxDelay)
	if err != nil {
		return sessionmemoryapp.Config{}, fmt.Errorf("balda.session_memory.retry.max_delay: %w", err)
	}
	progressInterval, err := optionalSessionMemoryDuration(cfg.Retry.ProgressInterval)
	if err != nil {
		return sessionmemoryapp.Config{}, fmt.Errorf("balda.session_memory.retry.progress_interval: %w", err)
	}
	fetchErrorDelay, err := optionalSessionMemoryDuration(cfg.Retry.FetchErrorDelay)
	if err != nil {
		return sessionmemoryapp.Config{}, fmt.Errorf("balda.session_memory.retry.fetch_error_delay: %w", err)
	}
	shutdownTimeout, err := optionalSessionMemoryDuration(cfg.Retry.ShutdownTimeout)
	if err != nil {
		return sessionmemoryapp.Config{}, fmt.Errorf("balda.session_memory.retry.shutdown_timeout: %w", err)
	}
	return sessionmemoryapp.Config{
		Enabled:             cfg.Enabled,
		MaxAttempts:         cfg.Retry.MaxAttempts,
		RetryBaseDelay:      baseDelay,
		RetryMaxDelay:       maxDelay,
		ProgressInterval:    progressInterval,
		FetchErrorDelay:     fetchErrorDelay,
		ShutdownTimeout:     shutdownTimeout,
		MaxConcurrentScopes: cfg.MaxConcurrentScopes,
		MaxQueuedPerScope:   cfg.MaxQueuedPerScope,
	}, nil
}

func sessionMemoryIngressOutboxConfig(cfg SessionMemoryConfig) (sessionmemoryapp.IngressOutboxConfig, error) {
	if err := validateSessionMemoryConfig(cfg); err != nil {
		return sessionmemoryapp.IngressOutboxConfig{}, err
	}
	if !cfg.Enabled {
		return sessionmemoryapp.IngressOutboxConfig{}, nil
	}
	baseDelay, err := optionalSessionMemoryDuration(cfg.Retry.BaseDelay)
	if err != nil {
		return sessionmemoryapp.IngressOutboxConfig{}, fmt.Errorf("balda.session_memory.retry.base_delay: %w", err)
	}
	maxDelay, err := optionalSessionMemoryDuration(cfg.Retry.MaxDelay)
	if err != nil {
		return sessionmemoryapp.IngressOutboxConfig{}, fmt.Errorf("balda.session_memory.retry.max_delay: %w", err)
	}
	return sessionmemoryapp.IngressOutboxConfig{
		Enabled:        true,
		MaxAttempts:    cfg.Retry.MaxAttempts,
		RetryBaseDelay: baseDelay,
		RetryMaxDelay:  maxDelay,
	}, nil
}

func sessionMemorySearchTimeout(cfg SessionMemoryConfig) (time.Duration, error) {
	if !cfg.Enabled {
		return defaultSessionMemorySearchTimeout, nil
	}
	if strings.TrimSpace(cfg.SearchTimeout) == "" {
		return defaultSessionMemorySearchTimeout, nil
	}
	value, err := parseSessionMemoryDuration(cfg.SearchTimeout)
	if err != nil || value <= 0 {
		if err == nil {
			err = fmt.Errorf("must be greater than zero")
		}
		return 0, fmt.Errorf("balda.session_memory.search_timeout: %w", err)
	}
	return value, nil
}

func optionalSessionMemoryDuration(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	return parseSessionMemoryDuration(raw)
}

func parseSessionMemoryDuration(raw string) (time.Duration, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasSuffix(trimmed, "d") {
		value := strings.TrimSpace(strings.TrimSuffix(trimmed, "d"))
		days, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", raw)
		}
		if days <= 0 || days > float64(math.MaxInt64)/float64(24*time.Hour) {
			return 0, fmt.Errorf("duration must be positive and bounded")
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(trimmed)
}

func parseSessionMemoryLimit(raw string, allowUnlimited bool) (int64, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if allowUnlimited && trimmed == "-1" {
		return -1, nil
	}
	if trimmed == "" {
		return 0, fmt.Errorf("value is required")
	}
	multipliers := []struct {
		suffix string
		value  int64
	}{
		{"gib", 1024 * 1024 * 1024}, {"gb", 1000 * 1000 * 1000},
		{"mib", 1024 * 1024}, {"mb", 1000 * 1000},
		{"kib", 1024}, {"kb", 1000}, {"b", 1},
	}
	for _, item := range multipliers {
		if strings.HasSuffix(trimmed, item.suffix) {
			value := strings.TrimSpace(strings.TrimSuffix(trimmed, item.suffix))
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil || parsed <= 0 || parsed > float64(math.MaxInt64)/float64(item.value) {
				return 0, fmt.Errorf("invalid byte value %q", raw)
			}
			return int64(parsed * float64(item.value)), nil
		}
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid byte value %q", raw)
	}
	return parsed, nil
}
