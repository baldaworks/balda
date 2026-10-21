package progressfmt

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

func TestTelegramUsesRichMarkdown(t *testing.T) {
	presentation, err := deliveryfmt.RenderStructured(t.Context(), mustRegistry(t), deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[Request]{
		Descriptor: RequestDescriptor,
		Body: Request{Progress: deliverycmd.Progress{
			Kind:    deliverycmd.ProgressPlanUpdate,
			Text:    "plan",
			Visible: true,
		}},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("delivery format = %q", presentation.DeliveryFormat)
	}
	if presentation.Text != "plan" {
		t.Fatalf("text = %q", presentation.Text)
	}
}

func TestZulipUsesPlainText(t *testing.T) {
	presentation, err := deliveryfmt.RenderStructured(t.Context(), mustRegistry(t), deliveryfmt.TransportZulip, deliveryfmt.StructuredEnvelope[Request]{
		Descriptor: RequestDescriptor,
		Body: Request{Progress: deliverycmd.Progress{
			Kind:    deliverycmd.ProgressPlanUpdate,
			Text:    "plan",
			Visible: true,
		}},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
		t.Fatalf("delivery format = %q", presentation.DeliveryFormat)
	}
}

func TestTechnicalProgressUsesStructuredRenderer(t *testing.T) {
	presentation, err := deliveryfmt.RenderStructured(t.Context(), mustRegistry(t), deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[Request]{
		Descriptor: RequestDescriptor,
		Body: Request{Progress: deliverycmd.Progress{
			Kind:     deliverycmd.ProgressTechnical,
			Text:     "Working",
			Audience: deliverycmd.ProgressAudience{UserID: "user-42"},
			Visible:  true,
		}},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.Text != "Working" || presentation.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("presentation = %+v", presentation)
	}
}

func mustRegistry(t *testing.T) *deliveryfmt.StructuredRegistry {
	t.Helper()
	reg, err := NewStructuredRegistry()
	if err != nil {
		t.Fatalf("NewStructuredRegistry() error = %v", err)
	}
	if err := deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportTelegram, RequestDescriptor, testTelegramProgressRenderer{}); err != nil {
		t.Fatalf("RegisterProgressRenderer() error = %v", err)
	}
	return reg
}

type testTelegramProgressRenderer struct{}

func (testTelegramProgressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           strings.TrimSpace(env.Body.Progress.Text),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}
