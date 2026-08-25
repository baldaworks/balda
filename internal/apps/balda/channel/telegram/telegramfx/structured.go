package telegramfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
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

func NewQuestionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, questionfmt.RequestDescriptor, telegramQuestionRenderer{})
}

func NewPermissionStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, permissionfmt.RequestDescriptor, telegramPermissionRenderer{})
}

func NewProgressStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportTelegram, progressfmt.RequestDescriptor, telegramProgressRenderer{})
}
