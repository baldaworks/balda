// Package logging provides application-wide logging configuration.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	LevelTrace = "trace"
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Init initializes the global logger for zerolog and configures slog for third-party libraries.
func Init(setters ...OptOptionsSetter) error {
	opts := NewOptions(setters...)
	if err := opts.Validate(); err != nil {
		return fmt.Errorf("validate logging options: %w", err)
	}
	levelName := LevelInfo
	zlLevel := zerolog.InfoLevel
	slogLevel := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(opts.level)) {
	case "", LevelInfo:
	case LevelTrace:
		levelName = LevelTrace
		zlLevel = zerolog.TraceLevel
		slogLevel = slog.LevelDebug - 4
	case LevelDebug:
		levelName = LevelDebug
		zlLevel = zerolog.DebugLevel
		slogLevel = slog.LevelDebug
	case LevelWarn, "warning":
		levelName = LevelWarn
		zlLevel = zerolog.WarnLevel
		slogLevel = slog.LevelWarn
	case LevelError:
		levelName = LevelError
		zlLevel = zerolog.ErrorLevel
		slogLevel = slog.LevelError
	default:
		return fmt.Errorf("resolve logging level: unsupported level %q (allowed: trace, debug, info, warn, error)", opts.level)
	}
	opts.level = levelName

	// 1. Configure zerolog (Primary for the project)
	zerolog.SetGlobalLevel(zlLevel)

	var zl zerolog.Logger
	if !opts.json {
		zl = zerolog.New(zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: time.RFC3339,
		})
	} else {
		zl = zerolog.New(os.Stderr)
	}

	zl = zl.With().Timestamp().Logger()
	log.Logger = zl
	zerolog.DefaultContextLogger = &log.Logger

	// 2. Configure slog (Only for third-party libraries)
	var slogHandler slog.Handler
	if !opts.json {
		slogHandler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slogLevel,
		})
	} else {
		slogHandler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: slogLevel,
		})
	}

	// Set as default so third-party libs using slog will use this configuration.
	slog.SetDefault(slog.New(slogHandler))

	return nil
}
