package questionfmt

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
)

func TestRenderTelegramKeepsPromptOnly(t *testing.T) {
	presentation := Render(Request{
		Prompt:  "Continue deployment?",
		Options: []questioncmd.Option{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
	}, deliveryfmt.TransportTelegram)
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("delivery format = %q", presentation.DeliveryFormat)
	}
	if presentation.Prompt != "Continue deployment?" {
		t.Fatalf("prompt = %q", presentation.Prompt)
	}
}

func TestRenderSlackAppendsOptions(t *testing.T) {
	presentation := Render(Request{
		Prompt:  "Continue deployment?",
		Options: []questioncmd.Option{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
	}, deliveryfmt.TransportSlack)
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
