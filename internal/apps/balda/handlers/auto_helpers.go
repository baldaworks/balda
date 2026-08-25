package handlers

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

type autoStateManager interface {
	RuntimeStateValue(ctx context.Context, locator baldasession.SessionLocator, key string) (any, bool, error)
}

const (
	autoActionOn  = "on"
	autoActionOff = "off"
)

func loadAutoStatus(ctx context.Context, sessions autoStateManager, locator baldasession.SessionLocator) (automode.Status, error) {
	return loadAutoStatusWithDefault(ctx, sessions, locator, automode.DefaultMaxTurns)
}

func loadAutoStatusWithDefault(ctx context.Context, sessions autoStateManager, locator baldasession.SessionLocator, defaultMaxTurns int) (automode.Status, error) {
	status := automode.DefaultStatusWithMaxTurns(defaultMaxTurns)
	if sessions == nil {
		return status, nil
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyEnabled); err != nil {
		return status, err
	} else if ok {
		status.Enabled = automode.ParseBool(value)
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyMode); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.State = strings.TrimSpace(text)
		}
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyConsecutiveTurns); err != nil {
		return status, err
	} else if ok {
		status.ConsecutiveTurns = automode.ParseInt(value, 0)
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyMaxTurns); err != nil {
		return status, err
	} else if ok {
		status.MaxTurns = automode.ParseInt(value, automode.NormalizeMaxTurns(defaultMaxTurns))
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyLastTurnAt); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.LastTurnAt = strings.TrimSpace(text)
		}
	}
	if value, ok, err := sessions.RuntimeStateValue(ctx, locator, automode.StateKeyLastStopReason); err != nil {
		return status, err
	} else if ok {
		if text, ok := value.(string); ok {
			status.LastStopReason = strings.TrimSpace(text)
		}
	}
	return automode.NormalizeWithDefault(status, defaultMaxTurns), nil
}
