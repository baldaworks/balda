package commandfx

import (
	"context"
	"errors"

	commandskill "github.com/baldaworks/balda/internal/apps/balda/actors/command/skill"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type skillCommandAdmission struct {
	skills    *baldaagent.SkillManager
	admission SkillAdmission
}

// NewSkillCommandAdmission validates selectors before the input and command are admitted.
func NewSkillCommandAdmission(skills *baldaagent.SkillManager, admission SkillAdmission) SkillCommandAdmission {
	return &skillCommandAdmission{skills: skills, admission: admission}
}

func (a *skillCommandAdmission) AdmitSkillCommand(ctx context.Context, payload commandcmd.Payload) (commandcmd.Payload, error) {
	if !payload.Access.SessionCommands {
		return payload, nil
	}
	selector, _, err := commandskill.ParseInvocation(payload.Args)
	if err != nil {
		return payload, nil
	} // The command actor owns user-facing validation responses.
	bound, err := a.skills.BindSnapshot(ctx, payload.SnapshotID)
	if err != nil {
		return payload, err
	}
	agentSelector := baldaagent.SkillSelector{Name: selector.Name}
	if selector.Plugin != "" {
		agentSelector.Source = runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: selector.Plugin}
	}
	_, err = bound.Resolve(agentSelector)
	if errors.Is(err, baldaagent.ErrSkillNotFound) || errors.Is(err, baldaagent.ErrSkillAmbiguous) || errors.Is(err, baldaagent.ErrSkillRevisionUnavailable) {
		return payload, nil
	}
	if err != nil {
		return payload, err
	}
	payload.ArchiveID, err = a.admission.AdmitSkillInput(ctx, payload)
	return payload, err
}
