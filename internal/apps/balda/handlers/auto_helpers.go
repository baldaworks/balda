package handlers

import (
	"context"
	"strings"
	"time"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/balda/internal/apps/balda/automode"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
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
	return automode.NormalizeWithDefault(status, defaultMaxTurns), nil
}

func plainAutoCommandReply(
	ctx context.Context,
	sessions autoStateManager,
	dispatcher actortransport.Dispatcher,
	locator baldasession.SessionLocator,
	args string,
	usage string,
	now time.Time,
	defaultMaxTurns int,
) string {
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "":
		status, err := loadAutoStatusWithDefault(ctx, sessions, locator, defaultMaxTurns)
		if err != nil {
			return "Could not read auto mode status."
		}
		return automode.RenderStatus(status)
	case autoActionOn:
		if err := dispatchAutoStateUpdate(ctx, dispatcher, locator, automode.EnableStateWithMaxTurns(now, defaultMaxTurns)); err != nil {
			return "Could not enable auto mode."
		}
		return automode.RenderStatus(automode.NormalizeWithDefault(automode.Status{
			Enabled:  true,
			State:    automode.StateIdle,
			MaxTurns: defaultMaxTurns,
		}, defaultMaxTurns))
	case autoActionOff:
		if err := dispatchAutoStateUpdate(ctx, dispatcher, locator, automode.DisableState()); err != nil {
			return "Could not disable auto mode."
		}
		return automode.RenderStatus(automode.DefaultStatusWithMaxTurns(defaultMaxTurns))
	default:
		return usage
	}
}
