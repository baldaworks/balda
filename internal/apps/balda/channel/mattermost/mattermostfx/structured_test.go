package mattermostfx

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
	"go.uber.org/fx"
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

func TestTurnFailureStructuredRegistrar(t *testing.T) {
	t.Parallel()
	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewTurnFailureStructuredRegistrar()(registry); err != nil {
		t.Fatalf("register turn failure: %v", err)
	}
	got, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportMattermost, deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]{
		Descriptor: deliveryfmt.TurnFailureDescriptor,
		Body:       deliveryfmt.TurnFailure{Text: "Provider error:\nmodel busy"},
	})
	if err != nil {
		t.Fatalf("render turn failure: %v", err)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
		t.Fatalf("DeliveryFormat = %q", got.DeliveryFormat)
	}
	if got.Text != "```\nProvider error:\nmodel busy\n```" {
		t.Fatalf("rendered failure = %q", got.Text)
	}
	// Use the production formatter registry and transport contribution. Failure
	// presentation must retain the fenced body through identity formatting.
	var formats deliveryfmt.PromptRegistry
	app := fx.New(fx.NopLogger, deliveryfx.Module,
		fx.Provide(fx.Annotate(NewPromptRegistryContribution, fx.ResultTags(`group:"balda_delivery_prompt_contribution"`))),
		fx.Populate(&formats),
	)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	name, _, formatter, err := formats.Resolve(deliveryfmt.TransportMattermost, got.DeliveryFormat)
	if err != nil {
		t.Fatal(err)
	}
	message, err := formatter.Format(got.Text)
	if err != nil {
		t.Fatal(err)
	}
	if name != deliveryfmt.NamePlainText || message.Name != deliveryfmt.NamePlainText || message.Text != got.Text || message.PlainFallback != got.Text {
		t.Fatalf("failure must keep identity presentation, name=%s message=%+v", name, message)
	}
}
