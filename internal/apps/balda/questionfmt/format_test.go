package questionfmt

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
)

func TestRenderTelegramKeepsPromptOnly(t *testing.T) {
	reg := deliveryfmt.NewStructuredRegistry()
	if err := deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportTelegram, RequestDescriptor, testTelegramQuestionRenderer{}); err != nil {
		t.Fatalf("RegisterQuestionRenderer() error = %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), reg, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[Request]{
		Descriptor: RequestDescriptor,
		Body: Request{
			Prompt:  "Continue deployment?",
			Options: []questioncmd.Option{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
		},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("delivery format = %q", presentation.DeliveryFormat)
	}
	if presentation.Text != "Continue deployment?" {
		t.Fatalf("prompt = %q", presentation.Text)
	}
}

type testTelegramQuestionRenderer struct{}

func (testTelegramQuestionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           strings.TrimSpace(env.Body.Prompt),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

func TestRenderSlackAppendsOptions(t *testing.T) {
	presentation := Render(Request{
		Prompt:  "Continue deployment?",
		Options: []questioncmd.Option{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
	}, deliveryfmt.TransportSlackAgent)
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
		t.Fatalf("delivery format = %q", presentation.DeliveryFormat)
	}
	for _, want := range []string{"Continue deployment?", "1. Yes", "2. No", "Reply with the number or option name."} {
		if !strings.Contains(presentation.Prompt, want) {
			t.Fatalf("prompt missing %q: %q", want, presentation.Prompt)
		}
	}
}

func TestRenderPlainFallbackAppendsOptions(t *testing.T) {
	presentation := Render(Request{
		Prompt:  "Continue deployment?",
		Options: []questioncmd.Option{{ID: "yes", Label: "Yes"}},
	}, deliveryfmt.TransportZulip)
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
		t.Fatalf("delivery format = %q", presentation.DeliveryFormat)
	}
	if strings.Contains(presentation.Prompt, "**") || !strings.Contains(presentation.Prompt, "1. Yes") {
		t.Fatalf("prompt = %q", presentation.Prompt)
	}
}
