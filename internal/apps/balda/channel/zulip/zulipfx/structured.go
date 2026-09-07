package zulipfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
)

type zulipLocatorRenderer struct{}

func (zulipLocatorRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[locatorfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderLocator(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

// NewLocatorStructuredRegistrar registers Zulip locator response presentation.
func NewLocatorStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportZulip, locatorfmt.ResponseDescriptor, zulipLocatorRenderer{})
}

type zulipGoalProgressRenderer struct{}

func (zulipGoalProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[goalkeepercmd.GoalProgress]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderGoalProgress(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

type zulipGoalOutcomeRenderer struct{}

func (zulipGoalOutcomeRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[goalkeepercmd.GoalOutcome]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderGoalOutcome(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

// NewGoalProgressStructuredRegistrar registers Zulip goal progress presentation.
func NewGoalProgressStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportZulip, goalkeepercmd.ProgressDescriptor, zulipGoalProgressRenderer{})
}

// NewGoalOutcomeStructuredRegistrar registers Zulip goal outcome presentation.
func NewGoalOutcomeStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportZulip, goalkeepercmd.OutcomeDescriptor, zulipGoalOutcomeRenderer{})
}

