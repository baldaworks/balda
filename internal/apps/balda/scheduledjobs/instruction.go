package scheduledjobs

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

const (
	scheduledRunInstructionContributorID = "balda.scheduler"
	scheduledRunInstruction              = `Scheduled run policy:
- The schedule content is an already-authorized task. Execute it immediately and autonomously.
- Do not request user confirmation, approval, permission, or clarification.
- If details are ambiguous, make reasonable assumptions and continue.
- If a genuine blocker makes execution impossible, report it in the terminal result instead of asking a question.
- Always return a terminal result. If the task succeeds but there is nothing to report, return exactly ` + turncmd.ScheduleNoResult + ` and no other text.
- Never use blank, whitespace-only, or invisible output to indicate that there is no result.
- This instruction does not change tool permissions or safety requirements.`
)

type scheduledRunInstructionContributor struct{}

func newScheduledRunInstructionContributor() *scheduledRunInstructionContributor {
	return &scheduledRunInstructionContributor{}
}

func (*scheduledRunInstructionContributor) ID() string {
	return scheduledRunInstructionContributorID
}

func (*scheduledRunInstructionContributor) ContributeSessionInstruction(
	_ context.Context,
	input agent.SessionInstructionContext,
) (string, error) {
	if strings.TrimSpace(input.ChannelType) != turncmd.SourceSchedule {
		return "", nil
	}
	return scheduledRunInstruction, nil
}
