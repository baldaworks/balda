package commandfx

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

// CommandIngress adapts actortransport.Dispatcher to commandcmd.Ingress.
type CommandIngress struct {
	dispatcher actortransport.Dispatcher
	snapshots  EffectiveSnapshotResolver
}

// SnapshotRequest contains only the trusted session binding needed for scope selection.
type SnapshotRequest struct {
	SessionID string
}

// EffectiveSnapshotResolver selects a trusted effective snapshot for ingress context.
type EffectiveSnapshotResolver interface {
	ResolveEffectiveSnapshot(ctx context.Context, request SnapshotRequest) (runtimecatalogcmd.SnapshotID, error)
}

// NewCommandIngress constructs a CommandIngress.
func NewCommandIngress(dispatcher actortransport.Dispatcher, snapshots EffectiveSnapshotResolver) *CommandIngress {
	return &CommandIngress{dispatcher: dispatcher, snapshots: snapshots}
}

// PublishCommand validates and publishes one command request to the command actor.
func (h *CommandIngress) PublishCommand(ctx context.Context, req commandcmd.Request) error {
	if h == nil || h.dispatcher == nil {
		return fmt.Errorf("command dispatcher is required")
	}
	if h.snapshots == nil {
		return fmt.Errorf("command snapshot resolver is required")
	}
	snapshotID, err := h.snapshots.ResolveEffectiveSnapshot(ctx, SnapshotRequest{SessionID: strings.TrimSpace(req.Payload.Locator.SessionID)})
	if err != nil {
		return fmt.Errorf("resolve command snapshot: %w", err)
	}
	if strings.TrimSpace(string(snapshotID)) == "" {
		return fmt.Errorf("resolve command snapshot: %w", runtimecatalogcmd.ErrSnapshotUnavailable)
	}
	req.Payload.Version = commandcmd.SchemaVersion
	req.Payload.SnapshotID = snapshotID
	id := strings.TrimSpace(req.InvocationID)
	env, err := commandcmd.NewEnvelope(req.Payload, commandcmd.EnvelopeOptions{
		ID:            id,
		DedupeKey:     id,
		CorrelationID: id,
		From:          actorlayer.ActorAddress{Target: "ingress", Key: strings.TrimSpace(req.Payload.Transport)},
	})
	if err != nil {
		return fmt.Errorf("build command envelope: %w", err)
	}
	if _, err := h.dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("dispatch command envelope: %w", err)
	}
	return nil
}
