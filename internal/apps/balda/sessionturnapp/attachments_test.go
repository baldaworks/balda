package sessionturnapp

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/attachment"
)

const (
	testJPEGMIMEType  = "image/jpeg"
	testVoiceMIMEType = "audio/ogg"
)

func TestBuildUserContent_LinksVoiceAttachment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "voice.ogg")
	data := []byte("voice bytes")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	content, err := buildUserContent("transcribe this", []attachment.Descriptor{{
		Kind:      attachment.KindVoice,
		SizeBytes: int64(len(data)),
		Blob:      &attachment.BlobRef{Path: path},
	}})
	if err != nil {
		t.Fatalf("buildUserContent() error = %v", err)
	}
	if len(content.Parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(content.Parts))
	}
	if content.Parts[2].FileData == nil {
		t.Fatal("voice file data = nil, want resource link source")
	}
	if got := content.Parts[2].FileData.MIMEType; got != testVoiceMIMEType {
		t.Fatalf("voice MIME type = %q, want %s", got, testVoiceMIMEType)
	}
	if got := content.Parts[2].FileData.FileURI; got != (&url.URL{Scheme: "file", Path: path}).String() {
		t.Fatalf("voice URI = %q, want file URI for %q", got, path)
	}
}

func TestBuildUserContent_PreservesExplicitVoiceMIME(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "voice.bin")
	if err := os.WriteFile(path, []byte("voice bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	content, err := buildUserContent("listen", []attachment.Descriptor{{
		Kind:     attachment.KindVoice,
		MIMEType: "audio/opus",
		Blob:     &attachment.BlobRef{Path: path},
	}})
	if err != nil {
		t.Fatalf("buildUserContent() error = %v", err)
	}
	if got := content.Parts[2].FileData.MIMEType; got != "audio/opus" {
		t.Fatalf("voice MIME type = %q, want audio/opus", got)
	}
}

func TestBuildUserContent_InlinesPhotoAttachment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "image.jpg")
	data := []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	content, err := buildUserContent("What is in this image?", []attachment.Descriptor{{
		Kind:      attachment.KindPhoto,
		SizeBytes: int64(len(data)),
		Caption:   "caption",
		Blob:      &attachment.BlobRef{Path: path, Key: "blob-key", SHA256: "deadbeef"},
	}})
	if err != nil {
		t.Fatalf("buildUserContent() error = %v", err)
	}
	if len(content.Parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(content.Parts))
	}
	if got := content.Parts[0].Text; got != "What is in this image?" {
		t.Fatalf("part[0].text = %q, want original text", got)
	}
	if got := content.Parts[1].Text; !strings.Contains(got, "Attachment: kind=photo") || !strings.Contains(got, "caption=caption") {
		t.Fatalf("part[1].text = %q, want attachment fallback metadata", got)
	}
	if content.Parts[2].InlineData == nil {
		t.Fatal("part[2].inline_data = nil, want bytes")
	}
	if got := content.Parts[2].InlineData.MIMEType; got != testJPEGMIMEType {
		t.Fatalf("part[2].inline_data.mime_type = %q, want %s", got, testJPEGMIMEType)
	}
}

func TestBuildUserContent_LinksDocumentAttachment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "doc report.pdf")
	data := []byte("%PDF-1.4\n%test\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	content, err := buildUserContent("", []attachment.Descriptor{{
		Kind:      attachment.KindDocument,
		FileName:  "doc report.pdf",
		SizeBytes: int64(len(data)),
		Blob:      &attachment.BlobRef{Path: path},
	}})
	if err != nil {
		t.Fatalf("buildUserContent() error = %v", err)
	}
	if len(content.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(content.Parts))
	}
	if got := content.Parts[0].Text; !strings.Contains(got, "Attachment: kind=document") {
		t.Fatalf("part[0].text = %q, want document fallback metadata", got)
	}
	if content.Parts[1].FileData == nil {
		t.Fatal("part[1].file_data = nil, want resource link source")
	}
	if got := content.Parts[1].FileData.MIMEType; got != "application/pdf" {
		t.Fatalf("part[1].file_data.mime_type = %q, want application/pdf", got)
	}
	if got := content.Parts[1].FileData.DisplayName; got != "doc report.pdf" {
		t.Fatalf("part[1].file_data.display_name = %q, want doc report.pdf", got)
	}
	if got := content.Parts[1].FileData.FileURI; got != (&url.URL{Scheme: "file", Path: path}).String() {
		t.Fatalf("part[1].file_data.file_uri = %q, want escaped URI for %q", got, path)
	}
}

func TestBuildUserContent_LinksOversizedDocument(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "large.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := file.Truncate(maxInlineAttachmentBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	content, err := buildUserContent("inspect", []attachment.Descriptor{{
		Kind:      attachment.KindDocument,
		FileName:  "large.json",
		MIMEType:  "application/json",
		SizeBytes: maxInlineAttachmentBytes + 1,
		Blob:      &attachment.BlobRef{Path: path},
	}})
	if err != nil {
		t.Fatalf("buildUserContent() error = %v", err)
	}
	if len(content.Parts) != 3 || content.Parts[2].FileData == nil {
		t.Fatalf("parts = %#v, want text, fallback, and file data", content.Parts)
	}
}

func TestBuildUserContent_OversizedAttachmentFallsBackToTextOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "large.jpg")
	data := make([]byte, maxInlineAttachmentBytes+1)
	data[0], data[1], data[2] = 0xff, 0xd8, 0xff
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	content, err := buildUserContent("hello", []attachment.Descriptor{{
		Kind:      attachment.KindPhoto,
		SizeBytes: int64(len(data)),
		Blob:      &attachment.BlobRef{Path: path},
	}})
	if err != nil {
		t.Fatalf("buildUserContent() error = %v", err)
	}
	if len(content.Parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(content.Parts))
	}
	if content.Parts[1].InlineData != nil {
		t.Fatal("part[1].inline_data != nil, want text-only fallback")
	}
	if got := content.Parts[1].Text; !strings.Contains(got, "Attachment: kind=photo") {
		t.Fatalf("part[1].text = %q, want fallback metadata", got)
	}
}
