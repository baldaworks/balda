package presentation

import (
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
)

const (
	maxContentLength = 4096
	maxLocations     = 10
)

func RenderPermission(body permissioncmd.Request) string {
	var out strings.Builder
	writeMarkdownRequest(&out, body)
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
