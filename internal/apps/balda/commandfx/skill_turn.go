package commandfx

import (
	"context"
	"errors"
	"fmt"
	"strings"

	commandskill "github.com/baldaworks/balda/internal/apps/balda/actors/command/skill"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

// SkillTurnExecutor resolves session-pinned skills and publishes normal turns.
type SkillTurnExecutor struct {
	skills     *baldaagent.SkillManager
	dispatcher actortransport.Dispatcher
}

// NewSkillTurnExecutor creates the composition adapter for the skill command.
func NewSkillTurnExecutor(skills *baldaagent.SkillManager, dispatcher actortransport.Dispatcher) *SkillTurnExecutor {
	return &SkillTurnExecutor{skills: skills, dispatcher: dispatcher}
}

// ExecuteSkill resolves one selector without loading its body and publishes its pinned turn.
func (e *SkillTurnExecutor) ExecuteSkill(
	ctx context.Context,
	parent actorlayer.Envelope,
	payload commandcmd.Payload,
	selector commandskill.Selector,
	prompt string,
) error {
	if e == nil || e.skills == nil || e.dispatcher == nil {
		return commandskill.ErrRuntimeUnavailable
	}

	bound, err := e.skills.BindSnapshot(ctx, payload.SnapshotID)
	if err != nil {
		return mapSkillResolutionError("bind skill snapshot", err)
	}

	agentSelector := baldaagent.SkillSelector{Name: selector.Name}
	if selector.Plugin != "" {
		agentSelector.Source = runtimecatalogcmd.SourceID{
			Kind: runtimecatalogcmd.SourceKindPlugin,
			Name: selector.Plugin,
		}
	}
	selection, err := bound.Resolve(agentSelector)
	if err != nil {
		return mapSkillResolutionError("resolve skill", err)
	}

	turn := turncmd.SessionTurnPayload{
		Text:            strings.TrimSpace(prompt),
		Locator:         payload.Locator,
		UserID:          payload.Principal,
		RequesterUserID: payload.Principal,
		DeliveryFormat:  payload.Presentation.DeliveryFormat,
		ProgressPolicy:  payload.Presentation.ProgressPolicy,
		Deliver:         true,
		Source:          payload.Transport,
		DedupeKey:       firstNonEmpty(parent.DedupeKey, parent.ID) + ":skill-turn",
		Skill:           &selection,
	}
	envelope, err := turncmd.SessionTurnEnvelope(turn)
	if err != nil {
		return fmt.Errorf("build skill turn: %w", err)
	}
	envelope.CorrelationID = firstNonEmpty(parent.CorrelationID, parent.ID)
	envelope.CausationID = parent.ID
	receipt, err := e.dispatcher.Dispatch(ctx, envelope)
	if err != nil {
		return fmt.Errorf("dispatch skill turn: %w", err)
	}
	if receipt == nil {
		return errors.New("skill turn dispatch returned no receipt")
	}
	return nil
}

func mapSkillResolutionError(operation string, err error) error {
	switch {
	case errors.Is(err, baldaagent.ErrSkillNotFound):
		return fmt.Errorf("%s: %w", operation, commandskill.ErrNotFound)
	case errors.Is(err, baldaagent.ErrSkillAmbiguous):
		return fmt.Errorf("%s: %w", operation, commandskill.ErrAmbiguous)
	case errors.Is(err, baldaagent.ErrSkillRevisionUnavailable), errors.Is(err, runtimecatalogcmd.ErrSnapshotUnavailable):
		return fmt.Errorf("%s: %w", operation, commandskill.ErrRevisionUnavailable)
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

var _ commandskill.Executor = (*SkillTurnExecutor)(nil)
