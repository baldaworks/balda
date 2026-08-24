// Package questionfmt renders structured question requests for concrete
// delivery channels without relying on model-authored formatting.
package questionfmt

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
)

type Request struct {
	Prompt  string
	Options []questioncmd.Option
}

type Presentation struct {
	Prompt         string
	DeliveryFormat deliveryfmt.DeliveryFormat
}

var RequestDescriptor = deliveryfmt.Descriptor[Request]{
	Type: "balda.question.request.v1",
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
		{transport: deliveryfmt.TransportSlack, renderer: plainOptionsRenderer{}},
		{transport: deliveryfmt.TransportZulip, renderer: plainOptionsRenderer{}},
	} {
		if err := deliveryfmt.RegisterStructuredRenderer(reg, registration.transport, RequestDescriptor, registration.renderer); err != nil {
			return err
		}
	}
	return nil
}

func Render(request Request, transport string) Presentation {
	reg, err := NewStructuredRegistry()
	if err != nil {
		return Presentation{Prompt: renderPlainOptions(request), DeliveryFormat: deliveryfmt.DeliveryFormatNone}
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), reg, strings.ToLower(strings.TrimSpace(transport)), deliveryfmt.StructuredEnvelope[Request]{
		Descriptor: RequestDescriptor,
		Body:       request,
	})
	if err != nil {
		return Presentation{Prompt: renderPlainOptions(request), DeliveryFormat: deliveryfmt.DeliveryFormatNone}
	}
	return Presentation{
		Prompt:         presentation.Text,
		DeliveryFormat: presentation.DeliveryFormat,
	}
}

type telegramRenderer struct{}

func (telegramRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           strings.TrimSpace(env.Body.Prompt),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type plainOptionsRenderer struct{}

func (plainOptionsRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           renderPlainOptions(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatNone,
	}, nil
}

func RenderMarkdownOptions(request Request) string {
	return renderMarkdownOptions(request)
}

func renderMarkdownOptions(request Request) string {
	var out strings.Builder
	out.WriteString(strings.TrimSpace(request.Prompt))
	writeOptions(&out, request.Options, "\n\n**Choose:**")
	out.WriteString("\n\n_Reply with the number or option name._")
	return strings.TrimSpace(out.String())
}

func renderPlainOptions(request Request) string {
	var out strings.Builder
	out.WriteString(strings.TrimSpace(request.Prompt))
	writeOptions(&out, request.Options, "\n\nChoose:")
	if len(request.Options) > 0 {
		out.WriteString("\n\nReply with the number or option name.")
	}
	return strings.TrimSpace(out.String())
}

func writeOptions(out *strings.Builder, options []questioncmd.Option, heading string) {
	if len(options) == 0 {
		return
	}
	out.WriteString(heading)
	for index, option := range options {
		fmt.Fprintf(out, "\n%d. %s", index+1, optionLabel(option, index))
	}
}

func optionLabel(option questioncmd.Option, index int) string {
	label := strings.TrimSpace(option.Label)
	if label != "" {
		return label
	}
	if strings.TrimSpace(option.ID) != "" {
		return strings.TrimSpace(option.ID)
	}
	return strconv.Itoa(index + 1)
}
