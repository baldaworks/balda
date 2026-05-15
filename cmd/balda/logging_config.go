package main

import (
	"strings"

	"github.com/normahq/balda/internal/apps/balda"
	"github.com/normahq/balda/internal/logging"
)

type baldaLoggingSettings struct {
	level string
	json  bool
}

func resolveBaldaLoggingSettings(cfg balda.LoggerConfig, debugFlag, traceFlag bool) baldaLoggingSettings {
	level := strings.TrimSpace(cfg.Level)
	if level == "" {
		level = logging.LevelInfo
	}
	if debugFlag {
		level = logging.LevelDebug
	}
	if traceFlag {
		level = logging.LevelTrace
	}

	return baldaLoggingSettings{
		level: level,
		json:  !cfg.Pretty,
	}
}

func applyBaldaLogging(cfg balda.LoggerConfig) error {
	settings := resolveBaldaLoggingSettings(cfg, debug, trace)
	return logging.Init(
		logging.WithLevel(settings.level),
		logging.WithJson(settings.json),
	)
}
