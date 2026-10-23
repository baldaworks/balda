package telegramfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
)

type telegramQuestionRenderer struct{}

func (telegramQuestionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[questionfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderQuestion(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type telegramPermissionRenderer struct{}

func (telegramPermissionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderPermission(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type telegramProgressRenderer struct{}

func (telegramProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[progressfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderProgress(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type telegramLocatorRenderer struct{}

type telegramStatusRenderer struct{}

func (telegramLocatorRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[locatorfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderLocator(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

func (telegramStatusRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[statusfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{Text: presentation.RenderStatus(env.Body), DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown}, nil
}

func NewQuestionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, questionfmt.RequestDescriptor, telegramQuestionRenderer{})
}

func NewPermissionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, permissionfmt.RequestDescriptor, telegramPermissionRenderer{})
}

func NewProgressStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, progressfmt.RequestDescriptor, telegramProgressRenderer{})
}

// NewLocatorStructuredRegistrar registers Telegram locator response presentation.
func NewLocatorStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, locatorfmt.ResponseDescriptor, telegramLocatorRenderer{})
}

// NewStatusStructuredRegistrar registers Telegram status response presentation.
func NewStatusStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return func(registry *deliveryfmt.StructuredRegistry) error {
		if err := deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportTelegram, statusfmt.ResponseDescriptor, telegramStatusRenderer{}); err != nil {
			return err
		}
		return deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportTelegram, statusfmt.LegacyResponseDescriptor, telegramStatusRenderer{})
	}
}

type telegramGoalProgressRenderer struct{}

func (telegramGoalProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[goalkeepercmd.GoalProgress]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderGoalProgress(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type telegramGoalOutcomeRenderer struct{}

func (telegramGoalOutcomeRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[goalkeepercmd.GoalOutcome]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderGoalOutcome(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

// NewGoalProgressStructuredRegistrar registers Telegram goal progress presentation.
func NewGoalProgressStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, goalkeepercmd.ProgressDescriptor, telegramGoalProgressRenderer{})
}

// NewGoalOutcomeStructuredRegistrar registers Telegram goal outcome presentation.
func NewGoalOutcomeStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, goalkeepercmd.OutcomeDescriptor, telegramGoalOutcomeRenderer{})
}

type telegramTurnFailureRenderer struct{}

func (telegramTurnFailureRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderTurnFailure(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatNone,
	}, nil
}

// NewTurnFailureStructuredRegistrar registers literal failed-final presentation.
func NewTurnFailureStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, deliveryfmt.TurnFailureDescriptor, telegramTurnFailureRenderer{})
}
