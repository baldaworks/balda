package actors

import (
	"context"
	"fmt"
	"strings"

	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionfmt"
	"github.com/baldaworks/go-actorlayer"
)

const (
	activationNoticeClass  = "notice"
	activationWarningClass = "warning"
)

// emitActivationMessages applies product notification policy after work has
// crossed the durable SessionTurn boundary. Invalid or mismatched lifecycle
// evidence rejects the turn; presentation and delivery failures are advisory.
func (e *SessionActorExecutor) emitActivationMessages(ctx context.Context, env actorlayer.Envelope, locator deliverycmd.Locator, activation *sessioncmd.Activation) error {
	if activation == nil {
		return nil
	}
	if err := validateActivationScope(env, locator, *activation); err != nil {
		return actorlayer.PolicyError(err)
	}

	if activation.Kind != sessioncmd.ActivationRestored {
		notice, err := sessionfmt.NewNotice(*activation)
		if err != nil {
			return actorlayer.PolicyError(fmt.Errorf("project session activation notice: %w", err))
		}
		typed, err := notice.Envelope()
		if err != nil {
			return actorlayer.PolicyError(fmt.Errorf("encode session activation notice: %w", err))
		}
		e.emitActivationMessage(ctx, env, locator, *activation, activationNoticeClass, "", sessionfmt.NoticeDescriptor.Type, typed)
	}
	for _, code := range activation.Warnings {
		warning, err := sessionfmt.NewWarning(activation.SessionID, code)
		if err != nil {
			return actorlayer.PolicyError(fmt.Errorf("project session activation warning: %w", err))
		}
		typed, err := warning.Envelope()
		if err != nil {
			return actorlayer.PolicyError(fmt.Errorf("encode session activation warning: %w", err))
		}
		e.emitActivationMessage(ctx, env, locator, *activation, activationWarningClass, code, sessionfmt.WarningDescriptor.Type, typed)
	}
	return nil
}

func validateActivationScope(env actorlayer.Envelope, locator deliverycmd.Locator, activation sessioncmd.Activation) error {
	if err := activation.Validate(); err != nil {
		return fmt.Errorf("invalid session activation: %w", err)
	}
	sessionID := strings.TrimSpace(locator.SessionID)
	if sessionID == "" || activation.SessionID != sessionID {
		return fmt.Errorf("session activation does not match authenticated locator")
	}
	canonical, err := deliverycmd.NewLocator(locator.ChannelType, locator.AddressKey, locator.AddressJSON, locator.SessionID)
	if err != nil || canonical != locator {
		return fmt.Errorf("session activation requires a canonical authenticated locator")
	}
	if !strings.EqualFold(strings.TrimSpace(env.To.Target), baldaexecution.ActorTypeSession) || strings.TrimSpace(env.To.Key) != sessionID {
		return fmt.Errorf("session activation does not match actor destination")
	}
	if envelopeSessionID := baldaexecution.EnvelopeSessionID(env); envelopeSessionID != sessionID {
		return fmt.Errorf("session activation does not match envelope scope")
	}
	return nil
}

func (e *SessionActorExecutor) emitActivationMessage(
	ctx context.Context,
	parent actorlayer.Envelope,
	locator deliverycmd.Locator,
	activation sessioncmd.Activation,
	messageClass string,
	warningCode sessioncmd.WarningCode,
	messageType deliveryfmt.MessageType,
	message any,
) {
	transport := strings.ToLower(strings.TrimSpace(locator.ChannelType))
	if e.registry == nil {
		e.logActivationMessageFailure("render", activation, messageClass, warningCode, messageType, transport, "registry_unavailable")
		return
	}
	presentation, err := e.registry.RenderStructured(ctx, transport, messageType, message)
	if err != nil {
		e.logActivationMessageFailure("render", activation, messageClass, warningCode, messageType, transport, "render_failed")
		return
	}
	delivery, err := deliverycmd.MarkdownEnvelopeWithFormatAndSettlement(
		"", parent.To, locator, presentation.DeliveryFormat, deliverycmd.SettlementBypass, presentation.Text, messageClass,
	)
	if err != nil {
		e.logActivationMessageFailure("build", activation, messageClass, warningCode, messageType, transport, "invalid_presentation")
		return
	}
	delivery.CorrelationID = parent.ID
	delivery.CausationID = parent.ID
	if e.dispatcher == nil {
		e.logActivationMessageFailure("dispatch", activation, messageClass, warningCode, messageType, transport, "dispatcher_unavailable")
		return
	}
	if _, err := e.dispatcher.Dispatch(ctx, delivery); err != nil {
		e.logActivationMessageFailure("dispatch", activation, messageClass, warningCode, messageType, transport, "dispatch_failed")
	}
}

func (e *SessionActorExecutor) logActivationMessageFailure(stage string, activation sessioncmd.Activation, messageClass string, warningCode sessioncmd.WarningCode, messageType deliveryfmt.MessageType, transport, code string) {
	event := e.logger.Warn().
		Str("stage", stage).
		Str("session_id", activation.SessionID).
		Str("message_class", messageClass).
		Str("message_type", string(messageType)).
		Str("transport", boundedActivationLogValue(transport)).
		Str("activation_kind", string(activation.Kind)).
		Str("failure_code", code)
	if warningCode != "" {
		event = event.Str("warning_code", string(warningCode))
	}
	event.Msg("session activation message failed")
}

func boundedActivationLogValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || strings.ContainsAny(value, "\x00\r\n") {
		return "invalid"
	}
	return value
}
