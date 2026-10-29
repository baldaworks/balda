// Package reset owns the transport-neutral fresh-lifetime command policy.
package reset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/lifetimeapp"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

const (
	commandName             = "reset"
	operationIDPrefix       = "fresh-lifetime-command:v1:"
	detachedDeliveryTimeout = 5 * time.Second
	msgDenied               = "Only the bot owner or collaborators can use this command."
	msgReset                = "A fresh local conversation is ready. Your provider, capabilities, and any requested model preference were preserved for later use."
	msgConflict             = "A fresh conversation request conflicts with another session operation. No new reset was started."
	msgUnavailable          = "A fresh conversation is currently unavailable. No new reset was started."
	msgInterrupted          = "The fresh conversation outcome is unconfirmed because the request was interrupted. Check /status before retrying /reset or /new."
	msgRecoveryRequired     = "This session requires recovery before another fresh conversation can be started."
)

// LifetimeSubmitter queues a fresh local lifetime through the session FIFO.
type LifetimeSubmitter interface {
	Submit(ctx context.Context, request lifetimeapp.Request) (<-chan lifetimeapp.Completion, int, error)
}

// Handler handles the bare /reset command and its /new alias.
type Handler struct {
	lifetimes       LifetimeSubmitter
	dispatcher      actortransport.Dispatcher
	deliveryTimeout time.Duration
}

// New constructs the fresh-lifetime command handler.
func New(lifetimes LifetimeSubmitter, dispatcher actortransport.Dispatcher) *Handler {
	return &Handler{lifetimes: lifetimes, dispatcher: dispatcher, deliveryTimeout: detachedDeliveryTimeout}
}

// Name returns the canonical command name.
func (*Handler) Name() string { return commandName }

// Handle authorizes and validates the command before submitting any session work.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, payload commandcmd.Payload) error {
	if !hasSessionAccess(payload) {
		return h.respond(ctx, env.ID, payload, msgDenied, "fresh-lifetime-denied")
	}
	if strings.TrimSpace(payload.Args) != "" {
		return h.respond(ctx, env.ID, payload, "Usage: "+usage(payload), "fresh-lifetime-usage")
	}
	if h.lifetimes == nil || strings.TrimSpace(env.ID) == "" {
		return h.respond(ctx, env.ID, payload, msgUnavailable, "fresh-lifetime-unavailable")
	}

	completion, _, err := h.lifetimes.Submit(ctx, lifetimeapp.Request{
		OperationID: lifetimeOperationID(env.ID),
		SessionID:   payload.Locator.SessionID,
		Command:     commandAlias(payload.Name),
	})
	if err != nil || completion == nil {
		return h.respond(ctx, env.ID, payload, msgUnavailable, "fresh-lifetime-unavailable")
	}
	completed, ok := awaitCompletion(ctx, completion)
	if !ok {
		return h.respond(ctx, env.ID, payload, msgInterrupted, "fresh-lifetime-interrupted")
	}
	message, suffix := resultMessage(completed.Result)
	return h.respond(ctx, env.ID, payload, message, suffix)
}

func resultMessage(result lifetimeapp.Result) (string, string) {
	switch result.Status {
	case lifetimeapp.StatusReset:
		return msgReset, "fresh-lifetime-reset"
	case lifetimeapp.StatusConflict:
		return msgConflict, "fresh-lifetime-conflict"
	case lifetimeapp.StatusUnavailable:
		return msgUnavailable, "fresh-lifetime-unavailable"
	case lifetimeapp.StatusInterrupted:
		return msgInterrupted, "fresh-lifetime-interrupted"
	case lifetimeapp.StatusRecoveryRequired:
		return msgRecoveryRequired, "fresh-lifetime-recovery-required"
	default:
		return msgUnavailable, "fresh-lifetime-unavailable"
	}
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
		return actorlayer.TransientError(fmt.Errorf("send fresh lifetime command response: %w", err))
	}
	return nil
}

func awaitCompletion(ctx context.Context, completion <-chan lifetimeapp.Completion) (lifetimeapp.Completion, bool) {
	select {
	case completed, ok := <-completion:
		return completed, ok
	default:
	}

	select {
	case completed, ok := <-completion:
		return completed, ok
	case <-ctx.Done():
		// Prefer authoritative completion when cancellation and completion race.
		select {
		case completed, ok := <-completion:
			return completed, ok
		default:
			return lifetimeapp.Completion{}, false
		}
	}
}

func lifetimeOperationID(envelopeID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(envelopeID)))
	return operationIDPrefix + hex.EncodeToString(digest[:])
}

func commandAlias(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "new" {
		return "new"
	}
	return commandName
}

func hasSessionAccess(payload commandcmd.Payload) bool {
	return payload.Access.SessionCommands || payload.Access.Owner || payload.Access.Collaborator || payload.Access.WorkspaceMember
}

func usage(payload commandcmd.Payload) string {
	name := commandAlias(payload.Name)
	root := strings.TrimSpace(payload.Invocation.Root)
	if root == "" || root == "/" {
		return "/" + name
	}
	return root + " " + name
}
