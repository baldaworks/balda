// Package model owns the transport-neutral model command policy.
package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/modelapp"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

const (
	commandName             = "model"
	operationIDPrefix       = "model-command:v1:"
	detachedDeliveryTimeout = 5 * time.Second
	sessionRecoveryRequired = "recovery_required"
	sessionRecoveryReady    = "ready"
	msgDenied               = "Only the bot owner or collaborators can use this command."
	msgUnavailable          = "Model control is currently unavailable. The current model was not changed."
	msgRecoveryRequired     = "This session requires recovery before its model can be changed."
	msgUnsupported          = "This provider does not support changing the model. The current model was not changed."
	msgIncompatible         = "That model is incompatible with this provider. The current model was not changed."
	msgRejected             = "The provider rejected that model. The current model was not changed."
	msgBusy                 = "This session is busy. The current model was not changed."
	msgConflict             = "The model change conflicts with another request. The current model was not changed."
)

// SessionReader reads committed session metadata without restoring a runtime.
type SessionReader interface {
	GetSessionInfo(ctx context.Context, sessionID string) (session.TopicSessionInfo, error)
}

// ModelSubmitter queues a model change through the session turn admission path.
type ModelSubmitter interface {
	Submit(ctx context.Context, request modelapp.Request) (<-chan modelapp.Completion, int, error)
}

// Handler handles the /model command.
type Handler struct {
	sessions        SessionReader
	models          ModelSubmitter
	dispatcher      actortransport.Dispatcher
	deliveryTimeout time.Duration
}

// New creates a model command handler.
func New(sessions SessionReader, models ModelSubmitter, dispatcher actortransport.Dispatcher) *Handler {
	return &Handler{
		sessions: sessions, models: models, dispatcher: dispatcher,
		deliveryTimeout: detachedDeliveryTimeout,
	}
}

// Name returns the canonical command name.
func (*Handler) Name() string { return commandName }

// Handle reads committed requested/active model state or queues one
// same-provider model change.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, payload commandcmd.Payload) error {
	if !hasSessionAccess(payload) {
		return h.respond(ctx, env.ID, payload, msgDenied, "model-denied")
	}

	args := strings.Fields(payload.Args)
	if len(args) > 1 || len(args) == 1 && namedSelector(args[0]) {
		return h.respond(ctx, env.ID, payload, usage(payload), "model-usage")
	}
	if len(args) == 0 {
		return h.read(ctx, env.ID, payload)
	}
	return h.change(ctx, env.ID, payload, args[0])
}

func (h *Handler) read(ctx context.Context, envelopeID string, payload commandcmd.Payload) error {
	if h.sessions == nil {
		return h.respond(ctx, envelopeID, payload, msgUnavailable, "model-unavailable")
	}
	info, err := h.sessions.GetSessionInfo(ctx, payload.Locator.SessionID)
	if errors.Is(err, session.ErrRuntimeRecoveryRequired) {
		return h.respond(ctx, envelopeID, payload, msgRecoveryRequired, "model-recovery-required")
	}
	if err != nil {
		return h.respond(ctx, envelopeID, payload, msgUnavailable, "model-unavailable")
	}
	if strings.EqualFold(strings.TrimSpace(info.Status), sessionRecoveryRequired) ||
		info.Recovery != "" && !strings.EqualFold(strings.TrimSpace(info.Recovery), sessionRecoveryReady) {
		return h.respond(ctx, envelopeID, payload, msgRecoveryRequired, "model-recovery-required")
	}
	return h.respond(ctx, envelopeID, payload, modelStateMessage(info), "model-current")
}

func modelStateMessage(info session.TopicSessionInfo) string {
	requested := "none (no explicit preference)"
	if info.RequestedModel != nil {
		if value := strings.TrimSpace(*info.RequestedModel); value != "" {
			state := "pending"
			if value == strings.TrimSpace(info.ActiveModel) && isAuthoritativeObservation(info.ActiveModelKind) {
				state = "active"
			}
			requested = value + " (" + state + ")"
		}
	}

	active := "unknown (not attached)"
	if value := strings.TrimSpace(info.ActiveModel); value != "" {
		switch info.ActiveModelKind {
		case session.ModelObservationProviderObserved:
			active = value + " (provider observed)"
		case session.ModelObservationLocallyExecuted:
			active = value + " (locally executed)"
		}
	}
	return "Requested: " + requested + ".\nActive: " + active + "."
}

func isAuthoritativeObservation(kind session.ModelObservationKind) bool {
	return kind == session.ModelObservationProviderObserved || kind == session.ModelObservationLocallyExecuted
}

func (h *Handler) change(ctx context.Context, envelopeID string, payload commandcmd.Payload, target string) error {
	if h.models == nil || strings.TrimSpace(envelopeID) == "" {
		return h.respond(ctx, envelopeID, payload, msgUnavailable, "model-unavailable")
	}
	completion, _, err := h.models.Submit(ctx, modelapp.Request{
		OperationID: modelOperationID(envelopeID),
		SessionID:   payload.Locator.SessionID,
		Model:       target,
	})
	if err != nil {
		if errors.Is(err, modelapp.ErrConflict) {
			return h.respond(ctx, envelopeID, payload, msgConflict, "model-conflict")
		}
		return h.respond(ctx, envelopeID, payload, msgUnavailable, "model-unavailable")
	}
	if completion == nil {
		return h.respond(ctx, envelopeID, payload, msgUnavailable, "model-unavailable")
	}

	completed, ok := awaitCompletion(ctx, completion)
	if !ok {
		return h.respond(ctx, envelopeID, payload, msgUnavailable, "model-unavailable")
	}
	if completed.Result.Status == session.ModelChangeRecoveryRequired {
		return h.respond(ctx, envelopeID, payload, msgRecoveryRequired, "model-recovery-required")
	}
	if completed.Err != nil {
		return h.respond(ctx, envelopeID, payload, msgUnavailable, "model-unavailable")
	}
	text, suffix := resultMessage(completed.Result)
	return h.respond(ctx, envelopeID, payload, text, suffix)
}

func resultMessage(result session.ModelChangeResult) (string, string) {
	switch result.Status {
	case session.ModelChangeChangedLive:
		if model := strings.TrimSpace(result.EffectiveModel); model != "" {
			return "Model changed live to " + model + ".", "model-changed-live"
		}
	case session.ModelChangeChangedReplacement:
		if model := strings.TrimSpace(result.EffectiveModel); model != "" {
			return "Model changed to " + model + " while preserving this session and its context.", "model-changed-replacement"
		}
	case session.ModelChangeUnsupported:
		return msgUnsupported, "model-unsupported"
	case session.ModelChangeIncompatible:
		return msgIncompatible, "model-incompatible"
	case session.ModelChangeRejected:
		return msgRejected, "model-rejected"
	case session.ModelChangeBusy:
		return msgBusy, "model-busy"
	case session.ModelChangeConflict:
		return msgConflict, "model-conflict"
	case session.ModelChangeUnavailable:
		return msgUnavailable, "model-unavailable"
	case session.ModelChangeRecoveryRequired:
		return msgRecoveryRequired, "model-recovery-required"
	}
	return msgUnavailable, "model-unavailable"
}

func (h *Handler) respond(ctx context.Context, operationID string, payload commandcmd.Payload, text, suffix string) error {
	deliveryCtx := ctx
	cancel := func() {}
	if ctx.Err() != nil {
		timeout := h.deliveryTimeout
		if timeout <= 0 {
			timeout = detachedDeliveryTimeout
		}
		deliveryCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), timeout)
	}
	defer cancel()
	if err := commandactor.SendPlain(deliveryCtx, h.dispatcher, operationID, payload.Locator, text, suffix); err != nil {
		return actorlayer.TransientError(fmt.Errorf("send model command response: %w", err))
	}
	return nil
}

func awaitCompletion(ctx context.Context, completion <-chan modelapp.Completion) (modelapp.Completion, bool) {
	select {
	case completed, ok := <-completion:
		return completed, ok
	default:
	}

	select {
	case completed, ok := <-completion:
		return completed, ok
	case <-ctx.Done():
		// A completion that became ready with cancellation is authoritative. The
		// final non-blocking read removes select's randomized tie-breaking.
		select {
		case completed, ok := <-completion:
			return completed, ok
		default:
			return modelapp.Completion{}, false
		}
	}
}

func modelOperationID(envelopeID string) string {
	digest := sha256.Sum256([]byte(envelopeID))
	return operationIDPrefix + hex.EncodeToString(digest[:])
}

func hasSessionAccess(payload commandcmd.Payload) bool {
	return payload.Access.SessionCommands || payload.Access.Owner || payload.Access.Collaborator || payload.Access.WorkspaceMember
}

func namedSelector(target string) bool {
	return strings.Contains(target, "=") || strings.HasPrefix(target, "--")
}

func usage(payload commandcmd.Payload) string {
	root := strings.TrimSpace(payload.Invocation.Root)
	if root == "" || root == "/" {
		return "Usage: /model [model-id]"
	}
	return "Usage: " + root + " model [model-id]"
}
