package mattermostfx

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
)

func TestQuestionStructuredRegistrarRendersMarkdownOptions(t *testing.T) {
	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewQuestionStructuredRegistrar()(registry); err != nil {
		t.Fatalf("NewQuestionStructuredRegistrar() error = %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportMattermost, deliveryfmt.StructuredEnvelope[questionfmt.Request]{
		Descriptor: questionfmt.RequestDescriptor,
		Body: questionfmt.Request{
			Prompt:  "Choose a deployment target.",
			Options: []questioncmd.Option{{ID: "prod", Label: "Production"}},
		},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if got, want := presentation.DeliveryFormat, deliveryfmt.DeliveryFormatMarkdown; got != want {
		t.Fatalf("DeliveryFormat = %q, want %q", got, want)
	}
	if !strings.Contains(presentation.Text, "Production") {
		t.Fatalf("presentation text = %q, want rendered option", presentation.Text)
	}
}
