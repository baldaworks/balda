package mattermostfx

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
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

func TestPermissionStructuredRegistrarRendersReplyInstructions(t *testing.T) {
	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewPermissionStructuredRegistrar()(registry); err != nil {
		t.Fatalf("NewPermissionStructuredRegistrar() error = %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportMattermost, deliveryfmt.StructuredEnvelope[permissioncmd.Request]{
		Descriptor: permissionfmt.RequestDescriptor,
		Body: permissioncmd.Request{
			ToolCall: permissioncmd.ToolCall{Title: "Run deployment"},
			Options:  []permissioncmd.Option{{ID: "allow", Name: "Allow once"}},
		},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if got, want := presentation.DeliveryFormat, deliveryfmt.DeliveryFormatMarkdown; got != want {
		t.Fatalf("DeliveryFormat = %q, want %q", got, want)
	}
	if !strings.Contains(presentation.Text, "Reply with the number or option name") {
		t.Fatalf("presentation text = %q, want reply instructions", presentation.Text)
	}
}
