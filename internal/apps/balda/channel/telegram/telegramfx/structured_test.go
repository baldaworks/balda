package telegramfx

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
)

func TestLocatorStructuredRegistrar(t *testing.T) {
	t.Parallel()

	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewLocatorStructuredRegistrar()(registry); err != nil {
		t.Fatalf("register locator renderer: %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[locatorfmt.Response]{
		Descriptor: locatorfmt.ResponseDescriptor,
		Body:       locatorfmt.Response{Transport: "telegram", Locator: "telegram:-1001:77"},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("DeliveryFormat = %q", presentation.DeliveryFormat)
	}
}

func TestStatusStructuredRegistrar(t *testing.T) {
	t.Parallel()
	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewStatusStructuredRegistrar()(registry); err != nil {
		t.Fatal(err)
	}
	got, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[statusfmt.Response]{
		Descriptor: statusfmt.ResponseDescriptor,
		Body:       statusfmt.Response{ProviderID: "codex", TurnActive: true, UserText: "**unsafe**", UserTextTruncated: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown || !strings.Contains(got.Text, "\\*\\*unsafe\\*\\*") || !strings.Contains(got.Text, "truncated") {
		t.Fatalf("presentation = %+v", got)
	}
	idle, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[statusfmt.Response]{Descriptor: statusfmt.ResponseDescriptor})
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
	presentation, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportTelegram, goalkeepercmd.ProgressEnvelope(goalkeepercmd.NewStartedProgress(5, "verify telegram goal")))
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("DeliveryFormat = %q", presentation.DeliveryFormat)
	}
}

func TestGoalOutcomeStructuredRegistrar(t *testing.T) {
	t.Parallel()

	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewGoalOutcomeStructuredRegistrar()(registry); err != nil {
		t.Fatalf("register goal outcome renderer: %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportTelegram, goalkeepercmd.OutcomeEnvelope(goalkeepercmd.GoalOutcome{
		GoalReached:  true,
		ExportStatus: goalkeepercmd.GoalExportStatusExported,
		WhatWasDone:  "Done on telegram.",
	}))
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("DeliveryFormat = %q", presentation.DeliveryFormat)
	}
}

func TestTurnFailureStructuredRegistrar(t *testing.T) {
	t.Parallel()
	registry := deliveryfmt.NewStructuredRegistry()
	if err := NewTurnFailureStructuredRegistrar()(registry); err != nil {
		t.Fatalf("register turn failure: %v", err)
	}
	got, err := deliveryfmt.RenderStructured(context.Background(), registry, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]{
		Descriptor: deliveryfmt.TurnFailureDescriptor,
		Body:       deliveryfmt.TurnFailure{Text: "Provider error:\nmodel busy"},
	})
	if err != nil {
		t.Fatalf("render turn failure: %v", err)
	}
	if got.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
		t.Fatalf("DeliveryFormat = %q", got.DeliveryFormat)
	}
	if got.Text != "Provider error:\nmodel busy" {
		t.Fatalf("rendered failure = %q", got.Text)
	}
}
