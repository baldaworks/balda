package sessionturnapp

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/attachment"
)

const testJPEGMIMEType = "image/jpeg"

func TestBuildUserContent_UsesFileDataForVoiceAttachment(t *testing.T) {
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
		t.Fatal("voice file data = nil, want persisted file reference")
	}
	if got := content.Parts[2].FileData.MIMEType; got != "audio/ogg" {
		t.Fatalf("voice MIME type = %q, want audio/ogg", got)
	}
	if got := content.Parts[2].FileData.FileURI; got != wantFileURI(path) {
		t.Fatalf("voice file URI = %q, want %q", got, wantFileURI(path))
	}
	if got := content.Parts[2].FileData.DisplayName; got != "voice.ogg" {
		t.Fatalf("voice display name = %q, want voice.ogg", got)
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

func TestBuildUserContent_UsesFileDataForPhotoAttachment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "photos and scans", "image.jpg")
	data := []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	content, err := buildUserContent("What is in this image?", []attachment.Descriptor{{
		Kind:      attachment.KindPhoto,
		FileName:  "image.jpg",
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
	if content.Parts[2].FileData == nil {
		t.Fatal("part[2].file_data = nil, want persisted file reference")
	}
	if got := content.Parts[2].FileData.MIMEType; got != testJPEGMIMEType {
		t.Fatalf("part[2].file_data.mime_type = %q, want %s", got, testJPEGMIMEType)
	}
	if got := content.Parts[2].FileData.FileURI; got != wantFileURI(path) {
		t.Fatalf("part[2].file_data.file_uri = %q, want %q", got, wantFileURI(path))
	}
	if got := content.Parts[2].FileData.FileURI; !strings.Contains(got, "%20") || strings.Contains(got, " ") {
		t.Fatalf("part[2].file_data.file_uri = %q, want escaped spaces", got)
	}
	if got := content.Parts[2].FileData.DisplayName; got != "image.jpg" {
		t.Fatalf("part[2].file_data.display_name = %q, want image.jpg", got)
	}
}

func TestBuildUserContent_UsesFileDataForDocumentAttachment(t *testing.T) {
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
		t.Fatal("part[1].file_data = nil, want persisted file reference")
	}
	if got := content.Parts[1].FileData.MIMEType; got != "application/pdf" {
		t.Fatalf("part[1].file_data.mime_type = %q, want application/pdf", got)
	}
	if got := content.Parts[1].FileData.FileURI; got != wantFileURI(path) {
		t.Fatalf("part[1].file_data.file_uri = %q, want %q", got, wantFileURI(path))
	}
	if got := content.Parts[1].FileData.DisplayName; got != "doc report.pdf" {
		t.Fatalf("part[1].file_data.display_name = %q, want doc report.pdf", got)
	}
}

func TestBuildUserContent_UsesFileDataForLargeAttachment(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "large.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	const size = 25 << 20
	if err := file.Truncate(size); err != nil {
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
		SizeBytes: size,
		Blob:      &attachment.BlobRef{Path: path},
	}})
	if err != nil {
		t.Fatalf("buildUserContent() error = %v", err)
	}
	if len(content.Parts) != 3 || content.Parts[2].FileData == nil {
		t.Fatalf("parts = %#v, want text, fallback, and file data", content.Parts)
	}
	if got := content.Parts[2].FileData.FileURI; got != wantFileURI(path) {
		t.Fatalf("large file URI = %q, want %q", got, wantFileURI(path))
	}
}

func TestBuildUserContent_MissingOrEmptyAttachmentIsDeterministic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.jpg")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	content, err := buildUserContent("hello", []attachment.Descriptor{{
		Kind: attachment.KindPhoto,
		Blob: &attachment.BlobRef{Path: emptyPath},
	}})
	if err != nil {
		t.Fatalf("buildUserContent(empty) error = %v", err)
	}
	if len(content.Parts) != 2 || content.Parts[1].FileData != nil || !strings.Contains(content.Parts[1].Text, "kind=photo") {
		t.Fatalf("empty attachment content = %#v, want metadata text fallback", content.Parts)
	}

	missingPath := filepath.Join(dir, "missing.jpg")
	if _, err := buildUserContent("hello", []attachment.Descriptor{{
		Kind: attachment.KindPhoto,
		Blob: &attachment.BlobRef{Path: missingPath},
	}}); err == nil || !strings.Contains(err.Error(), "open attachment blob") {
		t.Fatalf("buildUserContent(missing) error = %v, want open error", err)
	}
}

func wantFileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}
