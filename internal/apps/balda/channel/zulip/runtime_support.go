package zulip

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/automodecmd"
	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/usageview"
)

func dispatchOutbound(ctx context.Context, dispatcher actortransport.Dispatcher, env actorlayer.Envelope) error {
	if dispatcher == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	_, err := dispatcher.Dispatch(ctx, env)
	return err
}

func SendPlain(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string) error {
	env, err := deliverycmd.PlainEnvelopeWithSettlement("", from, locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func SendMarkdown(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string) error {
	env, err := deliverycmd.MarkdownEnvelopeWithSettlement("", from, locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func SendAgentReply(ctx context.Context, dispatcher actortransport.Dispatcher, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string) error {
	env, err := deliverycmd.AgentReplyEnvelopeWithFormatAndSettlement("", from, locator, "", deliverycmd.SettlementBypass, text, "")
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func SubmitSessionCancelControl(ctx context.Context, dispatcher actortransport.Dispatcher, locator baldasession.SessionLocator, requestedBy string, reason string, notify bool) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.CancelEnvelopeWithNotify(locator, "", requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build session cancel control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish session cancel control command: %w", err)
	}
	return nil
}

func SubmitSessionTurnCancelControl(ctx context.Context, dispatcher actortransport.Dispatcher, locator baldasession.SessionLocator, requestedBy string, reason string, notify bool) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.CancelTurnEnvelopeWithNotify(locator, requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build session turn cancel control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish session turn cancel control command: %w", err)
	}
	return nil
}

func SubmitGoalClearControl(ctx context.Context, dispatcher actortransport.Dispatcher, locator baldasession.SessionLocator, requestedBy string, reason string, notify bool) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.ClearGoalEnvelopeWithNotify(locator, requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build goal clear control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish goal clear control command: %w", err)
	}
	return nil
}

type autoStateManager interface {
	RuntimeStateValue(ctx context.Context, locator baldasession.SessionLocator, key string) (any, bool, error)
}

func PlainAutoCommandReply(
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
	case "on":
		if err := dispatchAutoStateUpdate(ctx, dispatcher, locator, automode.EnableStateWithMaxTurns(now, defaultMaxTurns)); err != nil {
			return "Could not enable auto mode."
		}
		return automode.RenderStatus(automode.NormalizeWithDefault(automode.Status{
			Enabled:  true,
			State:    automode.StateIdle,
			MaxTurns: defaultMaxTurns,
		}, defaultMaxTurns))
	case "off":
		if err := dispatchAutoStateUpdate(ctx, dispatcher, locator, automode.DisableState()); err != nil {
			return "Could not disable auto mode."
		}
		return automode.RenderStatus(automode.DefaultStatusWithMaxTurns(defaultMaxTurns))
	default:
		return usage
	}
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

func dispatchAutoStateUpdate(
	ctx context.Context,
	dispatcher actortransport.Dispatcher,
	locator baldasession.SessionLocator,
	state map[string]any,
) error {
	if dispatcher == nil || len(state) == 0 {
		return nil
	}
	env, err := automodecmd.Envelope(automodecmd.Payload{
		Locator: locator,
		State:   state,
	})
	if err != nil {
		return err
	}
	_, err = dispatcher.Dispatch(ctx, env)
	return err
}

func LoadUsageSnapshot(ctx context.Context, sessions autoStateManager, locator baldasession.SessionLocator) (usageview.Snapshot, bool, error) {
	if sessions == nil {
		return usageview.Snapshot{}, false, nil
	}
	value, ok, err := sessions.RuntimeStateValue(ctx, locator, usageview.UsageStateKey)
	if err != nil || !ok {
		return usageview.Snapshot{}, false, err
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return usageview.Snapshot{}, false, nil
	}
	return usageview.SnapshotFromMap(raw)
}

func RenderUsageSnapshot(snapshot usageview.Snapshot) string {
	return usageview.RenderSnapshot(snapshot)
}

type boundaryAwareSessionResetter interface {
	ResetSessionWithReason(ctx context.Context, locator baldasession.SessionLocator, reason baldasession.BoundaryReason) error
}

type sessionResetter interface {
	ResetSession(ctx context.Context, locator baldasession.SessionLocator) error
}

func ResetSessionWithReason(ctx context.Context, manager sessionResetter, locator baldasession.SessionLocator, reason baldasession.BoundaryReason) error {
	if aware, ok := manager.(boundaryAwareSessionResetter); ok {
		return aware.ResetSessionWithReason(ctx, locator, reason)
	}
	return manager.ResetSession(ctx, locator)
}
