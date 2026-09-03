package permissionfmt

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
)

func TestRenderTelegramUsesStructuredContentAndOmitsOpaqueInput(t *testing.T) {
	reg := deliveryfmt.NewStructuredRegistry()
	if err := deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportTelegram, RequestDescriptor, testTelegramPermissionRenderer{}); err != nil {
		t.Fatalf("RegisterPermissionRenderer() error = %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), reg, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[permissioncmd.Request]{
		Descriptor: RequestDescriptor,
		Body: permissioncmd.Request{
			Interaction: questioncmd.InteractionContext{Locator: deliverycmd.Locator{ChannelType: "telegram"}},
			ToolCall: permissioncmd.ToolCall{
				Title:    "Command approval",
				Kind:     "execute",
				RawInput: `{"threadId":"internal-id","token":"secret-value"}`,
				Content: []permissioncmd.Content{{
					Kind: permissioncmd.ContentKindText,
					Text: "Run the test.\n\nCommand:\n```sh\nid\n```\n\nWorking directory: `/workspace`",
				}},
			},
			Options: []permissioncmd.Option{{ID: "opt-1", Name: "Allow once"}, {ID: "opt-2", Name: "Cancel"}},
		},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("delivery format = %q", presentation.DeliveryFormat)
	}
	for _, want := range []string{"**Permission required**", "Run the test.", "```sh\nid\n```", "`/workspace`"} {
		if !strings.Contains(presentation.Text, want) {
			t.Fatalf("prompt missing %q: %q", want, presentation.Text)
		}
	}
	for _, hidden := range []string{"threadId", "internal-id", "secret-value", "opt-1", "opt-2", "RawInput", "1. Allow once", "2. Cancel", "Choose an action below"} {
		if strings.Contains(presentation.Text, hidden) {
			t.Fatalf("prompt exposed %q: %q", hidden, presentation.Text)
		}
	}
}

func TestRenderSlackAgentKeepsTextOptions(t *testing.T) {
	presentation := Render(permissioncmd.Request{
		Interaction: questioncmd.InteractionContext{Locator: deliverycmd.Locator{ChannelType: string(deliverycmd.ChannelTypeSlackAgent)}},
		ToolCall:    permissioncmd.ToolCall{Title: "Command approval"},
		Options:     []permissioncmd.Option{{ID: "allow", Name: "Allow"}, {ID: "cancel", Name: "Cancel"}},
	})
	if !strings.Contains(presentation.Prompt, "1. Allow") || !strings.Contains(presentation.Prompt, "2. Cancel") {
		t.Fatalf("prompt = %q, want text options", presentation.Prompt)
	}
}

func TestRenderOmitsGenericOtherKind(t *testing.T) {
	t.Parallel()

	reg := deliveryfmt.NewStructuredRegistry()
	if err := deliveryfmt.RegisterStructuredRenderer(reg, deliveryfmt.TransportTelegram, RequestDescriptor, testTelegramPermissionRenderer{}); err != nil {
		t.Fatalf("RegisterPermissionRenderer() error = %v", err)
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), reg, deliveryfmt.TransportTelegram, deliveryfmt.StructuredEnvelope[permissioncmd.Request]{
		Descriptor: RequestDescriptor,
		Body: permissioncmd.Request{
			Interaction: questioncmd.InteractionContext{Locator: deliverycmd.Locator{ChannelType: "telegram"}},
			ToolCall: permissioncmd.ToolCall{
				Title: "MCP elicitation request",
				Kind:  "other",
			},
		},
	})
	if err != nil {
		t.Fatalf("RenderStructured() error = %v", err)
	}
	if !strings.Contains(presentation.Text, "**Action:** MCP elicitation request") {
		t.Fatalf("prompt = %q, want action title", presentation.Text)
	}
	if strings.Contains(presentation.Text, "other") {
		t.Fatalf("prompt = %q, want generic kind omitted", presentation.Text)
	}
}

type testTelegramPermissionRenderer struct{}

func (testTelegramPermissionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	var out strings.Builder
	writeMarkdownRequest(&out, env.Body)
	return deliveryfmt.StructuredPresentation{
		Text:           out.String(),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

func TestRenderFallbackIsPlain(t *testing.T) {
	presentation := Render(permissioncmd.Request{
		Interaction: questioncmd.InteractionContext{Locator: deliverycmd.Locator{ChannelType: "zulip"}},
		ToolCall: permissioncmd.ToolCall{
			Title:   "Read file",
			Content: []permissioncmd.Content{{Kind: permissioncmd.ContentKindText, Text: "Inspect `config`"}},
		},
		Options: []permissioncmd.Option{{ID: "yes", Name: "Allow"}},
	})
	if presentation.DeliveryFormat != deliveryfmt.DeliveryFormatNone {
		t.Fatalf("delivery format = %q", presentation.DeliveryFormat)
	}
	if strings.Contains(presentation.Prompt, "**") || !strings.Contains(presentation.Prompt, "Inspect config") || !strings.Contains(presentation.Prompt, "1. Allow") {
		t.Fatalf("prompt = %q", presentation.Prompt)
	}
}
