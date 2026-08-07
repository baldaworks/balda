package sessionturnapp

import (
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/attachment"
	"google.golang.org/genai"
)

func buildUserContent(text string, attachments []attachment.Descriptor) (*genai.Content, error) {
	attachments = attachment.NormalizeList(attachments)
	parts := make([]*genai.Part, 0, 1+len(attachments)*2)
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		parts = append(parts, genai.NewPartFromText(trimmed))
	}
	for _, item := range attachments {
		filePart, fallbackText, err := buildAttachmentPart(item)
		if err != nil {
			return nil, err
		}
		if fallbackText != "" {
			parts = append(parts, genai.NewPartFromText(fallbackText))
		}
		if filePart != nil {
			parts = append(parts, filePart)
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
	path := strings.TrimSpace(item.Blob.Path)
	if !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("attachment blob path must be absolute: %q", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open attachment blob %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("stat attachment blob %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("attachment blob %q is not a regular file", path)
	}
	if info.Size() == 0 {
		return nil, fallback, nil
	}
	mimeType := detectAttachmentMIMEType(item)
	if mimeType == "" {
		return nil, fallback, nil
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	part := genai.NewPartFromURI(uri, mimeType)
	if part.FileData != nil {
		displayName := strings.TrimSpace(item.FileName)
		if displayName == "" {
			displayName = filepath.Base(path)
		}
		part.FileData.DisplayName = displayName
	}
	return part, fallback, nil
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

func detectAttachmentMIMEType(item attachment.Descriptor) string {
	if mimeType := strings.TrimSpace(item.MIMEType); mimeType != "" {
		return mimeType
	}
	if ext := strings.ToLower(filepath.Ext(item.FileName)); ext != "" {
		if detected := mime.TypeByExtension(ext); detected != "" {
			return detected
		}
	}
	switch item.Kind {
	case attachment.KindPhoto:
		return "image/jpeg"
	case attachment.KindDocument:
		return ""
	case attachment.KindVoice:
		return "audio/ogg"
	}
	return ""
}
