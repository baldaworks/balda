package telegramfx

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
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
