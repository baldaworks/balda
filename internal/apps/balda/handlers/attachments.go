package handlers

import (
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/attachment"
)

func appendAttachmentSummary(text string, attachments []attachment.Descriptor) string {
	attachments = attachment.NormalizeList(attachments)
	if len(attachments) == 0 {
		return text
	}
	var b strings.Builder
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	b.WriteString("Attachment manifest:\n")
	for i, item := range attachments {
		fmt.Fprintf(&b, "- attachment_%d:\n", i+1)
		fmt.Fprintf(&b, "  kind: %s\n", item.Kind)
		if item.FileName != "" {
			fmt.Fprintf(&b, "  file_name: %s\n", item.FileName)
		}
		if item.MIMEType != "" {
			fmt.Fprintf(&b, "  mime_type: %s\n", item.MIMEType)
		}
		if item.SizeBytes > 0 {
			fmt.Fprintf(&b, "  size_bytes: %d\n", item.SizeBytes)
		}
		if item.Caption != "" {
			fmt.Fprintf(&b, "  caption: %s\n", item.Caption)
		}
		if item.Blob != nil {
			if item.Blob.Store != "" {
				fmt.Fprintf(&b, "  blob_store: %s\n", item.Blob.Store)
			}
			if item.Blob.Path != "" {
				fmt.Fprintf(&b, "  local_path: %s\n", item.Blob.Path)
			}
			if item.Blob.Key != "" {
				fmt.Fprintf(&b, "  blob_key: %s\n", item.Blob.Key)
			}
			if item.Blob.SHA256 != "" {
				fmt.Fprintf(&b, "  sha256: %s\n", item.Blob.SHA256)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
