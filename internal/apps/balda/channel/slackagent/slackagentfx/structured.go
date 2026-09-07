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

func (slackAgentLocatorRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[locatorfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderLocator(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
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

// NewLocatorStructuredRegistrar registers Slack locator response presentation.
func NewLocatorStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportSlackAgent, locatorfmt.ResponseDescriptor, slackAgentLocatorRenderer{})
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

