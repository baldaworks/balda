package sessionturnapp

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/attachment"
	"google.golang.org/genai"
)

const maxInlineAttachmentBytes = 20 << 20

func buildUserContent(text string, attachments []attachment.Descriptor) (*genai.Content, error) {
	attachments = attachment.NormalizeList(attachments)
	parts := make([]*genai.Part, 0, 1+len(attachments)*2)
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		parts = append(parts, genai.NewPartFromText(trimmed))
	}
	for _, item := range attachments {
		inlinePart, fallbackText, err := buildAttachmentPart(item)
		if err != nil {
			return nil, err
		}
		if fallbackText != "" {
			parts = append(parts, genai.NewPartFromText(fallbackText))
		}
		if inlinePart != nil {
			parts = append(parts, inlinePart)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, genai.NewPartFromText(""))
	}
	return &genai.Content{Role: genai.RoleUser, Parts: parts}, nil
}

func buildAttachmentPart(item attachment.Descriptor) (*genai.Part, string, error) {
	fallback := attachmentFallbackText(item)
	if item.Blob == nil || strings.TrimSpace(item.Blob.Path) == "" {
		return nil, fallback, nil
	}
	data, err := os.ReadFile(item.Blob.Path)
	if err != nil {
		return nil, "", fmt.Errorf("read attachment blob %q: %w", item.Blob.Path, err)
	}
	if len(data) == 0 || len(data) > maxInlineAttachmentBytes {
		return nil, fallback, nil
	}
	mimeType := detectAttachmentMIMEType(item, data)
	if mimeType == "" {
		return nil, fallback, nil
	}
	return genai.NewPartFromBytes(data, mimeType), fallback, nil
}

func attachmentFallbackText(item attachment.Descriptor) string {
	lines := make([]string, 0, 8)
	lines = append(lines, fmt.Sprintf("Attachment: kind=%s", item.Kind))
	if item.FileName != "" {
		lines = append(lines, fmt.Sprintf("file_name=%s", item.FileName))
	}
	if item.MIMEType != "" {
		lines = append(lines, fmt.Sprintf("mime_type=%s", item.MIMEType))
	}
	if item.SizeBytes > 0 {
		lines = append(lines, fmt.Sprintf("size_bytes=%d", item.SizeBytes))
	}
	if item.Caption != "" {
		lines = append(lines, fmt.Sprintf("caption=%s", item.Caption))
	}
	if item.Blob != nil {
		if item.Blob.Path != "" {
			lines = append(lines, fmt.Sprintf("local_path=%s", item.Blob.Path))
		}
		if item.Blob.Key != "" {
			lines = append(lines, fmt.Sprintf("blob_key=%s", item.Blob.Key))
		}
		if item.Blob.SHA256 != "" {
			lines = append(lines, fmt.Sprintf("sha256=%s", item.Blob.SHA256))
		}
	}
	return strings.Join(lines, "\n")
}

func detectAttachmentMIMEType(item attachment.Descriptor, data []byte) string {
	if mimeType := strings.TrimSpace(item.MIMEType); mimeType != "" {
		return mimeType
	}
	switch item.Kind {
	case attachment.KindPhoto:
		if detected := http.DetectContentType(data); strings.HasPrefix(detected, "image/") {
			return detected
		}
		return "image/jpeg"
	case attachment.KindDocument:
		if detected := http.DetectContentType(data); detected != "" && detected != "application/octet-stream" {
			return detected
		}
		switch strings.ToLower(filepath.Ext(item.FileName)) {
		case ".pdf":
			return "application/pdf"
		case ".png":
			return "image/png"
		case ".jpg", ".jpeg":
			return "image/jpeg"
		}
	case attachment.KindVoice:
		return "audio/ogg"
	}
	return ""
}
