// Package permissionfmt renders structured permission requests for concrete
// delivery channels without inspecting opaque provider input.
package permissionfmt

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
)

const (
	maxContentLength = 4096
	maxLocations     = 10
)

type Presentation struct {
	Prompt         string
	DeliveryFormat deliveryfmt.DeliveryFormat
}

var RequestDescriptor = deliveryfmt.Descriptor[permissioncmd.Request]{
	Type: deliveryfmt.MessageTypePermissionRequest,
}

func NewStructuredRegistry() (*deliveryfmt.StructuredRegistry, error) {
	reg := deliveryfmt.NewStructuredRegistry()
	if err := RegisterStructuredRenderers(reg); err != nil {
		return nil, err
	}
	return reg, nil
}

func RegisterStructuredRenderers(reg *deliveryfmt.StructuredRegistry) error {
	for _, registration := range []struct {
		transport string
		renderer  deliveryfmt.StructuredRenderer[permissioncmd.Request]
	}{
		{transport: deliveryfmt.TransportTelegram, renderer: telegramRenderer{}},
		{transport: deliveryfmt.TransportSlack, renderer: plainRenderer{}},
		{transport: deliveryfmt.TransportZulip, renderer: plainRenderer{}},
	} {
		if err := deliveryfmt.RegisterStructuredRenderer(reg, registration.transport, RequestDescriptor, registration.renderer); err != nil {
			return err
		}
	}
	return nil
}

func Render(request permissioncmd.Request) Presentation {
	reg, err := NewStructuredRegistry()
	if err != nil {
		return Presentation{Prompt: renderPlain(request), DeliveryFormat: deliveryfmt.DeliveryFormatNone}
	}
	presentation, err := deliveryfmt.RenderStructured(context.Background(), reg, strings.ToLower(strings.TrimSpace(request.Interaction.Locator.ChannelType)), deliveryfmt.StructuredEnvelope[permissioncmd.Request]{
		Descriptor: RequestDescriptor,
		Body:       request,
	})
	if err != nil {
		return Presentation{Prompt: renderPlain(request), DeliveryFormat: deliveryfmt.DeliveryFormatNone}
	}
	return Presentation{Prompt: presentation.Text, DeliveryFormat: presentation.DeliveryFormat}
}

func renderTelegramMarkdown(request permissioncmd.Request) string {
	var out strings.Builder
	writeMarkdownRequest(&out, request)
	return out.String()
}

func renderMarkdown(request permissioncmd.Request) string {
	var out strings.Builder
	writeMarkdownRequest(&out, request)
	writeOptions(&out, request.Options, "\n\n**Choose:**")
	out.WriteString("\n\n_Reply with the number or option name._")
	return out.String()
}

func writeMarkdownRequest(out *strings.Builder, request permissioncmd.Request) {
	out.WriteString("🔐 **Permission required**")
	writeMarkdownAction(out, request.ToolCall)
	writeMarkdownContent(out, request.ToolCall.Content)
	writeMarkdownLocations(out, request.ToolCall.Locations)
}

func writeMarkdownAction(out *strings.Builder, toolCall permissioncmd.ToolCall) {
	title := displayValue(toolCall.Title)
	kind := displayKind(toolCall.Kind)
	if title == "" && kind == "" {
		return
	}
	out.WriteString("\n\n**Action:** ")
	if title != "" {
		out.WriteString(title)
	}
	if kind != "" {
		if title != "" {
			out.WriteString(" ")
		}
		out.WriteString("`")
		out.WriteString(strings.ReplaceAll(kind, "`", "'"))
		out.WriteString("`")
	}
}

func writeMarkdownContent(out *strings.Builder, content []permissioncmd.Content) {
	for _, item := range content {
		switch item.Kind {
		case permissioncmd.ContentKindText:
			if text := displayValue(item.Text); text != "" {
				out.WriteString("\n\n")
				out.WriteString(text)
			}
		case permissioncmd.ContentKindDiff:
			if path := displayValue(item.Path); path != "" {
				out.WriteString("\n\n**File change:** `")
				out.WriteString(strings.ReplaceAll(path, "`", "'"))
				out.WriteString("`")
			}
		case permissioncmd.ContentKindTerminal:
			if terminalID := displayValue(item.TerminalID); terminalID != "" {
				out.WriteString("\n\n**Terminal:** `")
				out.WriteString(strings.ReplaceAll(terminalID, "`", "'"))
				out.WriteString("`")
			}
		}
	}
}

func writeMarkdownLocations(out *strings.Builder, locations []permissioncmd.Location) {
	for index, location := range locations {
		if index >= maxLocations {
			break
		}
		path := displayValue(location.Path)
		if path == "" {
			continue
		}
		out.WriteString("\n\n**Affected:** `")
		out.WriteString(strings.ReplaceAll(path, "`", "'"))
		if location.Line != nil {
			out.WriteString(":")
			out.WriteString(strconv.Itoa(*location.Line))
		}
		out.WriteString("`")
	}
}

func renderPlain(request permissioncmd.Request) string {
	var out strings.Builder
	out.WriteString("Permission required")
	if title := displayValue(request.ToolCall.Title); title != "" {
		out.WriteString("\n\nAction: ")
		out.WriteString(title)
	}
	if kind := displayKind(request.ToolCall.Kind); kind != "" {
		out.WriteString("\nKind: ")
		out.WriteString(kind)
	}
	for _, item := range request.ToolCall.Content {
		if item.Kind != permissioncmd.ContentKindText {
			continue
		}
		if content := plainText(displayValue(item.Text)); content != "" {
			out.WriteString("\n\n")
			out.WriteString(content)
		}
	}
	for index, location := range request.ToolCall.Locations {
		if index >= maxLocations {
			break
		}
		path := displayValue(location.Path)
		if path == "" {
			continue
		}
		out.WriteString("\nAffected: ")
		out.WriteString(path)
		if location.Line != nil {
			out.WriteString(":")
			out.WriteString(strconv.Itoa(*location.Line))
		}
	}
	writeOptions(&out, request.Options, "\n\nChoose:")
	out.WriteString("\n\nReply with the number or option name.")
	return out.String()
}

func writeOptions(out *strings.Builder, options []permissioncmd.Option, heading string) {
	out.WriteString(heading)
	for index, option := range options {
		fmt.Fprintf(out, "\n%d. %s", index+1, displayValue(option.Name))
	}
}

func displayValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxContentLength {
		return value
	}
	return value[:maxContentLength] + "…"
}

func displayKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "other", "unknown":
		return ""
	default:
		return displayValue(value)
	}
}

func plainText(value string) string {
	value = strings.ReplaceAll(value, "```sh\n", "")
	value = strings.ReplaceAll(value, "```", "")
	return strings.ReplaceAll(value, "`", "")
}

type telegramRenderer struct{}

func (telegramRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           renderTelegramMarkdown(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
	}, nil
}

type plainRenderer struct{}

func (plainRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           renderPlain(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatNone,
	}, nil
}

func RenderMarkdown(request permissioncmd.Request) string {
	return renderMarkdown(request)
}
