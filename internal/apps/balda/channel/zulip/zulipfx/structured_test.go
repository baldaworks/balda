package zulipfx

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
	"go.uber.org/fx"
)

func TestLocatorStructuredRegistrar(t *testing.T) {
	t.Parallel()

	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewLocatorStructuredRegistrar()(registry); err != nil {
		t.Fatalf("register locator renderer: %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportZulip, deliveryfmt.StructuredEnvelope[locatorfmt.Response]{
		Descriptor: locatorfmt.ResponseDescriptor,
		Body:       locatorfmt.Response{Transport: "zulip", Locator: "zulip:s:42:deploys"},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatMarkdown {
		t.Fatalf("DeliveryFormat = %q", presentation.DeliveryFormat)
	}
}

func TestStatusStructuredRegistrar(t *testing.T) {
	t.Parallel()
	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewStatusStructuredRegistrar()(registry); err != nil {
		t.Fatal(err)
	}
	got, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportZulip, deliveryfmt.StructuredEnvelope[statusfmt.Response]{
		Descriptor: statusfmt.ResponseDescriptor,
		Body:       statusfmt.Response{ProviderID: "codex", TurnActive: true, UserText: "**unsafe**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatMarkdown || !strings.Contains(got.Text, "\\*\\*unsafe\\*\\*") {
		t.Fatalf("presentation = %+v", got)
	}
	idle, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportZulip, deliveryfmt.StructuredEnvelope[statusfmt.Response]{Descriptor: statusfmt.ResponseDescriptor})
	if err != nil || !strings.Contains(idle.Text, "idle") || strings.Contains(idle.Text, "User text") {
		t.Fatalf("idle presentation = %+v, error = %v", idle, err)
	}
}

func TestGoalProgressStructuredRegistrar(t *testing.T) {
	t.Parallel()

	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewGoalProgressStructuredRegistrar()(registry); err != nil {
		t.Fatalf("register goal progress renderer: %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportZulip, goalkeepercmd.ProgressEnvelope(goalkeepercmd.NewStartedProgress(5, "verify zulip goal")))
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatMarkdown {
		t.Fatalf("DeliveryFormat = %q", presentation.DeliveryFormat)
	}
}

func TestGoalOutcomeStructuredRegistrar(t *testing.T) {
	t.Parallel()

	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewGoalOutcomeStructuredRegistrar()(registry); err != nil {
		t.Fatalf("register goal outcome renderer: %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportZulip, goalkeepercmd.OutcomeEnvelope(goalkeepercmd.GoalOutcome{
		GoalReached:  true,
		ExportStatus: goalkeepercmd.GoalExportStatusExported,
		WhatWasDone:  "Done on zulip.",
	}))
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatMarkdown {
		t.Fatalf("DeliveryFormat = %q", presentation.DeliveryFormat)
	}
}

func TestTurnFailureStructuredRegistrar(t *testing.T) {
	t.Parallel()
	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewTurnFailureStructuredRegistrar()(registry); err != nil {
		t.Fatalf("register turn failure: %v", err)
	}
	got, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportZulip, deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]{
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
	name, _, formatter, err := formats.Resolve(deliveryfmt.TransportZulip, got.DeliveryFormat)
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
