// Package progressfmt renders structured progress updates for concrete
// delivery channels without relying on model-authored formatting.
package progressfmt

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

type Request struct {
	Progress deliverycmd.Progress
}

type Presentation struct {
	Text           string
	DeliveryFormat deliveryfmt.DeliveryFormat
}

var RequestDescriptor = deliveryfmt.Descriptor[Request]{
	Type: "balda.progress.update.v1",
}

func NewStructuredRegistry() (*deliveryfmt.StructuredRegistry, error) {
	reg := deliveryfmt.NewStructuredRegistry()
	if err := RegisterStructuredRenderers(reg); err != nil {
		return nil, err
	}
	return reg, nil
}

func RegisterStructuredRenderers(reg *deliveryfmt.StructuredRegistry) error {
	for _, registration := range []struct {
		transport string
		renderer  deliveryfmt.StructuredRenderer[Request]
	}{
		{transport: deliveryfmt.TransportTelegram, renderer: telegramRenderer{}},
		{transport: deliveryfmt.TransportSlack, renderer: plainRenderer{format: deliveryfmt.DeliveryFormatNone}},
		{transport: deliveryfmt.TransportZulip, renderer: plainRenderer{format: deliveryfmt.DeliveryFormatNone}},
	} {
		if err := deliveryfmt.RegisterStructuredRenderer(reg, registration.transport, RequestDescriptor, registration.renderer); err != nil {
			return err
		}
	}
	return nil
}

type telegramRenderer struct{}

func (telegramRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           strings.TrimSpace(env.Body.Progress.Text),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type plainRenderer struct {
	format deliveryfmt.DeliveryFormat
}

func (r plainRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           strings.TrimSpace(env.Body.Progress.Text),
		DeliveryFormat: r.format,
	}, nil
}

func RenderPlain(request Request) deliveryfmt.StructuredPresentation {
	return deliveryfmt.StructuredPresentation{
		Text:           strings.TrimSpace(request.Progress.Text),
		DeliveryFormat: deliveryfmt.DeliveryFormatNone,
	}
}
