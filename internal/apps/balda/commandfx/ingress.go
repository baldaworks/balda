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

// CommandDispatcher is the publication capability consumed by command ingress.
type CommandDispatcher interface {
	Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error)
}

// CommandIngress adapts actortransport.Dispatcher to commandcmd.Ingress.
// SkillCommandAdmission validates and records eligible skill input before publication.
type SkillCommandAdmission interface {
	AdmitSkillCommand(ctx context.Context, payload commandcmd.Payload) (commandcmd.Payload, error)
}

// SetSkillAdmission installs the production skill input admission boundary.
func (h *CommandIngress) SetSkillAdmission(admission SkillCommandAdmission) { h.skills = admission }

type CommandIngress struct {
	skills     SkillCommandAdmission
	dispatcher CommandDispatcher
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
func NewCommandIngress(dispatcher CommandDispatcher, snapshots EffectiveSnapshotResolver) *CommandIngress {
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
	if req.Payload.OriginalInput != nil {
		req.Payload.OriginalInput = req.Payload.OriginalInput.Clone()
		req.Payload.OriginalInput.Source = "command:" + strings.TrimSpace(req.InvocationID)
	}
	req.Payload.Version = commandcmd.SchemaVersion
	req.Payload.SnapshotID = snapshotID
	if strings.EqualFold(req.Payload.Name, "skill") && h.skills != nil {
		req.Payload, err = h.skills.AdmitSkillCommand(ctx, req.Payload)
		if err != nil {
			return err
		}
	}
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
