package slackagentfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/sessionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
)

type slackAgentQuestionRenderer struct{}

func (slackAgentQuestionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[questionfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderQuestion(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}

type slackAgentPermissionRenderer struct{}

func (slackAgentPermissionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderPermission(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}

type slackAgentProgressRenderer struct{}

func (slackAgentProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[progressfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderProgress(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatNone,
	}, nil
}

type slackAgentLocatorRenderer struct{}

type slackAgentStatusRenderer struct{}
type slackAgentLegacyStatusRenderer struct{}

func (slackAgentLocatorRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[locatorfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderLocator(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}

func (slackAgentStatusRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[statusfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{Text: presentation.RenderStatus(env.Body), DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn}, nil
}

func (slackAgentLegacyStatusRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[statusfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{Text: presentation.RenderLegacyStatus(env.Body), DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn}, nil
}

func NewQuestionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, questionfmt.RequestDescriptor, slackAgentQuestionRenderer{})
	}
}

func NewPermissionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, permissionfmt.RequestDescriptor, slackAgentPermissionRenderer{})
	}
}

func NewProgressStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, progressfmt.RequestDescriptor, slackAgentProgressRenderer{})
	}
}

type slackAgentSessionNoticeRenderer struct{}
type slackAgentSessionWarningRenderer struct{}

func (slackAgentSessionNoticeRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[sessionfmt.Notice]) (deliveryfmt.StructuredPresentation, error) {
	text, err := presentation.RenderSessionNotice(env.Body)
	return deliveryfmt.StructuredPresentation{Text: text, DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn}, err
}

func (slackAgentSessionWarningRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[sessionfmt.Warning]) (deliveryfmt.StructuredPresentation, error) {
	text, err := presentation.RenderSessionWarning(env.Body)
	return deliveryfmt.StructuredPresentation{Text: text, DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn}, err
}

// NewSessionStructuredRegistrar registers Slack session lifecycle presentation.
func NewSessionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		if err := deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, sessionfmt.NoticeDescriptor, slackAgentSessionNoticeRenderer{}); err != nil {
			return err
		}
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, sessionfmt.WarningDescriptor, slackAgentSessionWarningRenderer{})
	}
}

// NewSessionStructuredRequirements requires both lifecycle renderers at composition.
func NewSessionStructuredRequirements() []deliveryfx.StructuredRegistryRequirement {
	return []deliveryfx.StructuredRegistryRequirement{
		{Transport: deliveryfmt.TransportSlackAgent, MessageType: sessionfmt.NoticeDescriptor.Type},
		{Transport: deliveryfmt.TransportSlackAgent, MessageType: sessionfmt.WarningDescriptor.Type},
	}
}

// NewLocatorStructuredRegistrar registers Slack locator response presentation.
func NewLocatorStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, locatorfmt.ResponseDescriptor, slackAgentLocatorRenderer{})
	}
}

// NewStatusStructuredRegistrar registers Slack status response presentation.
func NewStatusStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		if err := deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, statusfmt.ResponseDescriptor, slackAgentStatusRenderer{}); err != nil {
			return err
		}
		if err := deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, statusfmt.LegacyV2ResponseDescriptor, slackAgentLegacyStatusRenderer{}); err != nil {
			return err
		}
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, statusfmt.LegacyResponseDescriptor, slackAgentLegacyStatusRenderer{})
	}
}

type slackAgentGoalProgressRenderer struct{}

func (slackAgentGoalProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[goalkeepercmd.GoalProgress]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderGoalProgress(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}

type slackAgentGoalOutcomeRenderer struct{}

func (slackAgentGoalOutcomeRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[goalkeepercmd.GoalOutcome]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderGoalOutcome(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}

// NewGoalProgressStructuredRegistrar registers Slack goal progress presentation.
func NewGoalProgressStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, goalkeepercmd.ProgressDescriptor, slackAgentGoalProgressRenderer{})
	}
}

// NewGoalOutcomeStructuredRegistrar registers Slack goal outcome presentation.
func NewGoalOutcomeStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, goalkeepercmd.OutcomeDescriptor, slackAgentGoalOutcomeRenderer{})
	}
}

type slackagentTurnFailureRenderer struct{}

func (slackagentTurnFailureRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderTurnFailure(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatNone,
	}, nil
}

// NewTurnFailureStructuredRegistrar registers literal failed-final presentation.
func NewTurnFailureStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportSlackAgent, deliveryfmt.TurnFailureDescriptor, slackagentTurnFailureRenderer{})
}
