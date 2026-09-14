package commandfx

import (
	"context"
	"fmt"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

// PluginTurnExecutor adapts a declarative plugin command to the normal session-turn path.
type PluginTurnExecutor struct {
	dispatcher actortransport.Dispatcher
}

// NewPluginTurnExecutor creates the CommandActor plugin adapter.
func NewPluginTurnExecutor(dispatcher actortransport.Dispatcher) *PluginTurnExecutor {
	return &PluginTurnExecutor{dispatcher: dispatcher}
}

// ExecutePluginCommand publishes exactly one revision-pinned child turn.
func (e *PluginTurnExecutor) ExecutePluginCommand(ctx context.Context, parent actorlayer.Envelope, payload commandcmd.Payload, descriptor runtimecatalogcmd.CommandDescriptor) error {
	if e == nil || e.dispatcher == nil || descriptor.Skill == nil {
		return actorlayer.TransientError(fmt.Errorf("plugin command runtime is unavailable"))
	}
	ref := *descriptor.Skill
	turn := turncmd.SessionTurnPayload{
		Text:            pluginInvocationText(payload),
		Locator:         payload.Locator,
		UserID:          payload.Principal,
		RequesterUserID: payload.Principal,
		DeliveryFormat:  payload.Presentation.DeliveryFormat,
		ProgressPolicy:  payload.Presentation.ProgressPolicy,
		Deliver:         true,
		Source:          payload.Transport,
		DedupeKey:       firstNonEmpty(parent.DedupeKey, parent.ID) + ":plugin-turn",
		Skill:           &runtimecatalogcmd.SkillSelection{Snapshot: payload.SnapshotID, Ref: ref},
	}
	envelope, err := turncmd.SessionTurnEnvelope(turn)
	if err != nil {
		return actorlayer.PolicyError(fmt.Errorf("build plugin command turn: %w", err))
	}
	envelope.CorrelationID = firstNonEmpty(parent.CorrelationID, parent.ID)
	envelope.CausationID = parent.ID
	receipt, err := e.dispatcher.Dispatch(ctx, envelope)
	if err != nil {
		return err
	}
	if receipt == nil {
		return actorlayer.TransientError(fmt.Errorf("plugin command dispatch returned no receipt"))
	}
	return nil
}

func pluginInvocationText(payload commandcmd.Payload) string {
	root := strings.TrimSpace(payload.Invocation.Root)
	name := strings.ToLower(strings.TrimSpace(payload.Name))
	var invocation string
	if root == "" || root == "/" {
		invocation = "/" + name
	} else {
		invocation = root + " " + name
	}
	if args := strings.TrimSpace(payload.Args); args != "" {
		invocation += " " + args
	}
	return invocation
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

var _ commandactor.PluginExecutor = (*PluginTurnExecutor)(nil)
