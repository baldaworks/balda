package scheduledjobs

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"go.uber.org/fx"
)

func TestScheduledRunInstructionContributorUsesCanonicalScheduleChannel(t *testing.T) {
	t.Parallel()

	contributor := newScheduledRunInstructionContributor()
	if got := contributor.ID(); got != scheduledRunInstructionContributorID {
		t.Fatalf("ID() = %q, want %q", got, scheduledRunInstructionContributorID)
	}

	tests := []struct {
		name        string
		channelType string
		want        bool
	}{
		{name: "schedule", channelType: turncmd.SourceSchedule, want: true},
		{name: "trimmed schedule", channelType: " schedule ", want: true},
		{name: "telegram", channelType: "telegram"},
		{name: "webhook", channelType: turncmd.SourceWebhook},
		{name: "empty"},
		{name: "non-canonical case", channelType: "SCHEDULE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := contributor.ContributeSessionInstruction(t.Context(), agent.SessionInstructionContext{
				SessionID:   "must-not-leak",
				ChannelType: test.channelType,
			})
			if err != nil {
				t.Fatalf("ContributeSessionInstruction() error = %v", err)
			}
			if (got != "") != test.want {
				t.Fatalf("ContributeSessionInstruction() = %q, want contribution=%t", got, test.want)
			}
			if test.want && got != scheduledRunInstruction {
				t.Fatalf("ContributeSessionInstruction() = %q, want exact scheduled policy", got)
			}
			if strings.Contains(got, "must-not-leak") {
				t.Fatalf("contribution leaked runtime context: %q", got)
			}
		})
	}
}

func TestScheduledRunInstructionStatesAutonomousExecutionContract(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"already-authorized task",
		"Execute it immediately and autonomously",
		"Do not request user confirmation, approval, permission, or clarification",
		"make reasonable assumptions and continue",
		"report it in the terminal result instead of asking a question",
		"Always return a terminal result",
		"return exactly " + turncmd.ScheduleNoResult + " and no other text",
		"Never use blank, whitespace-only, or invisible output",
		"does not change tool permissions or safety requirements",
	} {
		if !strings.Contains(scheduledRunInstruction, want) {
			t.Fatalf("scheduled instruction missing %q:\n%s", want, scheduledRunInstruction)
		}
	}
}

func TestScheduledRunInstructionProviderRegistersBuilderContributor(t *testing.T) {
	t.Parallel()

	var params struct {
		fx.In
		Contributors []agent.SessionInstructionContributor `group:"balda_session_instruction_contributors"`
	}
	app := fx.New(
		fx.Provide(scheduledRunInstructionProvider),
		fx.Populate(&params),
		fx.NopLogger,
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start Fx app: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Errorf("stop Fx app: %v", err)
		}
	})
	if len(params.Contributors) != 1 || params.Contributors[0].ID() != scheduledRunInstructionContributorID {
		t.Fatalf("contributors = %#v, want one scheduler contributor", params.Contributors)
	}
}
