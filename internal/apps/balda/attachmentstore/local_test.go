package attachmentstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
)

const voiceBlobExtension = ".ogg"

func TestLocalStorePersistsBoundedContent(t *testing.T) {
	stateDir := t.TempDir()
	store, err := New(Config{Engine: EngineLocal, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	body := []byte("voice bytes")
	descriptor := attachment.Descriptor{Kind: attachment.KindVoice, FileID: "voice-file-id"}

	got, err := store.Persist(context.Background(), descriptor, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("Persist() error = %v", err)
	}
	if got.Blob == nil {
		t.Fatal("blob = nil, want local blob reference")
	}
	if got.Blob.Store != EngineLocal {
		t.Fatalf("blob store = %q, want %q", got.Blob.Store, EngineLocal)
	}
	if ext := filepath.Ext(got.Blob.Path); ext != voiceBlobExtension {
		t.Fatalf("blob extension = %q, want %s", ext, voiceBlobExtension)
	}
	if got.SizeBytes != int64(len(body)) {
		t.Fatalf("size = %d, want %d", got.SizeBytes, len(body))
	}
	stored, err := os.ReadFile(got.Blob.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(stored, body) {
		t.Fatalf("stored bytes = %q, want %q", stored, body)
	}
	if !strings.HasPrefix(got.Blob.Path, filepath.Join(stateDir, "attachments")+string(filepath.Separator)) {
		t.Fatalf("blob path = %q, want below attachment root", got.Blob.Path)
	}
}

func TestLocalStorePreservesDescriptorMetadataAndReusesContent(t *testing.T) {
	store, err := New(Config{Engine: EngineLocal, StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	body := []byte("mp3 bytes")
	descriptor := attachment.Descriptor{
		Kind:      attachment.KindDocument,
		FileID:    "audio-file-id",
		FileName:  "sample.mp3",
		MIMEType:  "audio/mpeg",
		SizeBytes: 999,
	}

	first, err := store.Persist(context.Background(), descriptor, bytes.NewReader(body), 1024)
	if err != nil {
		t.Fatalf("first Persist() error = %v", err)
	}
	second, err := store.Persist(context.Background(), descriptor, bytes.NewReader(body), 1024)
	if err != nil {
		t.Fatalf("second Persist() error = %v", err)
	}
	if first.Blob == nil || second.Blob == nil || first.Blob.Path != second.Blob.Path {
		t.Fatalf("blob paths = %v and %v, want the same content-addressed path", first.Blob, second.Blob)
	}
	if first.FileName != descriptor.FileName || first.MIMEType != descriptor.MIMEType {
		t.Fatalf("metadata = (%q, %q), want (%q, %q)", first.FileName, first.MIMEType, descriptor.FileName, descriptor.MIMEType)
	}
	if first.SizeBytes != int64(len(body)) {
		t.Fatalf("actual size = %d, want %d", first.SizeBytes, len(body))
	}
	if ext := filepath.Ext(first.Blob.Path); ext != ".mp3" {
		t.Fatalf("blob extension = %q, want .mp3", ext)
	}
}

func TestLocalStoreRejectsOversizedContentWithoutPublishingBlob(t *testing.T) {
	stateDir := t.TempDir()
	store, err := New(Config{Engine: EngineLocal, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	descriptor := attachment.Descriptor{Kind: attachment.KindDocument, FileID: "large"}

	_, err = store.Persist(context.Background(), descriptor, strings.NewReader("12345"), 4)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Persist() error = %v, want ErrTooLarge", err)
	}
	assertNoRegularFiles(t, filepath.Join(stateDir, "attachments"))
}

func TestLocalStoreCancellationRemovesTemporaryFile(t *testing.T) {
	stateDir := t.TempDir()
	store, err := New(Config{Engine: EngineLocal, StateDir: stateDir})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	descriptor := attachment.Descriptor{Kind: attachment.KindDocument, FileID: "cancelled"}

	_, err = store.Persist(ctx, descriptor, strings.NewReader("body"), 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Persist() error = %v, want context.Canceled", err)
	}
	assertNoRegularFiles(t, filepath.Join(stateDir, "attachments"))
}

func TestOffStoreReturnsDisabled(t *testing.T) {
	store, err := New(Config{Engine: EngineOff})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if store.Enabled() {
		t.Fatal("Enabled() = true, want false")
	}
	_, err = store.Persist(context.Background(), attachment.Descriptor{Kind: attachment.KindDocument, FileID: "file"}, strings.NewReader("body"), 10)
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Persist() error = %v, want ErrDisabled", err)
	}
}

func TestExtensionFromDescriptorUsesOnlySafeSuffixes(t *testing.T) {
	tests := []struct {
		name       string
		descriptor attachment.Descriptor
		want       string
	}{
		{name: "filename", descriptor: attachment.Descriptor{FileName: "sample.MP3"}, want: ".MP3"},
		{name: "unsafe filename falls back to MIME", descriptor: attachment.Descriptor{FileName: "sample.bad!", MIMEType: "application/pdf"}, want: ".pdf"},
		{name: "voice MIME", descriptor: attachment.Descriptor{Kind: attachment.KindVoice, MIMEType: "audio/ogg"}, want: voiceBlobExtension},
		{name: "voice kind fallback", descriptor: attachment.Descriptor{Kind: attachment.KindVoice}, want: voiceBlobExtension},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := extensionFromDescriptor(test.descriptor); got != test.want {
				t.Fatalf("extensionFromDescriptor() = %q, want %q", got, test.want)
			}
		})
	}
}

func assertNoRegularFiles(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			t.Errorf("unexpected regular file after failed persistence: %s", path)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("WalkDir() error = %v", err)
	}
}
