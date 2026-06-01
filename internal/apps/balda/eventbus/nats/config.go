package natsbus

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	baldaeventbus "github.com/normahq/balda/internal/apps/balda/eventbus"
	"github.com/normahq/balda/internal/apps/balda/swarm"
)

type resolvedConfig struct {
	NATS      baldaeventbus.Config
	Swarm     swarm.Config
	StoreDir  string
	MaxMemory int64
	MaxStore  int64
	Commands  streamSpec
	Events    streamSpec
	DLQ       streamSpec
	AckWait   time.Duration
	FetchWait time.Duration
}

const embeddedNATSStateDirName = "nats"

type streamSpec struct {
	MaxAge     time.Duration
	MaxBytes   int64
	MaxMsgSize int32
	Discard    string
}

func resolveConfig(natsCfg baldaeventbus.Config, swarmCfg swarm.Config, stateDir string) (resolvedConfig, error) {
	normalizedNATS, err := natsCfg.Normalized()
	if err != nil {
		return resolvedConfig{}, err
	}
	normalizedSwarm, err := swarmCfg.Normalized()
	if err != nil {
		return resolvedConfig{}, err
	}
	out := resolvedConfig{NATS: normalizedNATS, Swarm: normalizedSwarm}
	trimmedStateDir := strings.TrimSpace(stateDir)
	if trimmedStateDir == "" {
		return resolvedConfig{}, fmt.Errorf("balda.state_dir is required")
	}
	out.StoreDir = filepath.Join(trimmedStateDir, embeddedNATSStateDirName)
	out.MaxMemory, err = parseBytes(normalizedNATS.MaxMemory)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("balda.nats.max_memory: %w", err)
	}
	out.MaxStore, err = parseBytes(normalizedNATS.MaxStore)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("balda.nats.max_store: %w", err)
	}
	out.Commands = streamSpec{MaxAge: 7 * 24 * time.Hour, MaxBytes: -1, MaxMsgSize: -1, Discard: "new"}
	out.Events = streamSpec{MaxAge: 30 * 24 * time.Hour, MaxBytes: -1, MaxMsgSize: -1, Discard: "old"}
	out.DLQ = streamSpec{MaxAge: 30 * 24 * time.Hour, MaxBytes: -1, MaxMsgSize: -1, Discard: "new"}
	out.AckWait, err = parseDuration(normalizedSwarm.Commands.AckWait)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("balda.swarm.commands.ack_wait: %w", err)
	}
	out.FetchWait, err = parseDuration(normalizedSwarm.Commands.FetchWait)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("balda.swarm.commands.fetch_wait: %w", err)
	}
	return out, nil
}

func parseDuration(raw string) (time.Duration, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasSuffix(trimmed, "d") {
		value := strings.TrimSpace(strings.TrimSuffix(trimmed, "d"))
		days, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", raw, err)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(trimmed)
}

func parseBytes(raw string) (int64, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return 0, fmt.Errorf("value is required")
	}
	multipliers := []struct {
		suffix string
		value  int64
	}{
		{"gib", 1024 * 1024 * 1024},
		{"gb", 1000 * 1000 * 1000},
		{"mib", 1024 * 1024},
		{"mb", 1000 * 1000},
		{"kib", 1024},
		{"kb", 1000},
		{"b", 1},
	}
	for _, item := range multipliers {
		if strings.HasSuffix(trimmed, item.suffix) {
			value := strings.TrimSpace(strings.TrimSuffix(trimmed, item.suffix))
			if value == "" {
				return 0, fmt.Errorf("invalid byte value %q", raw)
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid byte value %q: %w", raw, err)
			}
			if parsed < 0 {
				return 0, fmt.Errorf("byte value must be non-negative")
			}
			return int64(parsed * float64(item.value)), nil
		}
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid byte value %q: %w", raw, err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("byte value must be non-negative")
	}
	return parsed, nil
}
