package attachmentstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/attachment"
)

const voiceBlobExtension = ".ogg"

type recordingTelegramDownloader struct {
	fileIDs []string
	file    DownloadedFile
}

func (d *recordingTelegramDownloader) DownloadFile(_ context.Context, fileID string) (DownloadedFile, error) {
	d.fileIDs = append(d.fileIDs, fileID)
	return d.file, nil
}

func TestLocalStorePersistsVoiceWithOGGExtension(t *testing.T) {
	body := []byte("voice bytes")
	downloader := &recordingTelegramDownloader{
		file: DownloadedFile{
			FileID:    "voice-file-id",
			SizeBytes: int64(len(body)),
			Body:      body,
		},
	}
	store, err := New(Config{Engine: EngineLocal, StateDir: t.TempDir()}, downloader)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	got, err := store.PersistTelegram(context.Background(), []attachment.Descriptor{{
		Kind:   attachment.KindVoice,
		FileID: "voice-file-id",
	}})
	if err != nil {
		t.Fatalf("PersistTelegram() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("persisted descriptors = %d, want 1", len(got))
	}
	if len(downloader.fileIDs) != 1 || downloader.fileIDs[0] != "voice-file-id" {
		t.Fatalf("downloaded file IDs = %v, want [voice-file-id]", downloader.fileIDs)
	}
	if got[0].Blob == nil {
		t.Fatal("voice blob = nil, want local blob reference")
	}
	if ext := filepath.Ext(got[0].Blob.Path); ext != voiceBlobExtension {
		t.Fatalf("voice blob extension = %q, want %s", ext, voiceBlobExtension)
	}
	if got[0].SizeBytes != int64(len(body)) {
		t.Fatalf("voice size = %d, want %d", got[0].SizeBytes, len(body))
	}
	stored, err := os.ReadFile(got[0].Blob.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(stored) != string(body) {
		t.Fatalf("stored voice bytes = %q, want %q", stored, body)
	}
}

func TestExtensionFromDescriptorUsesVoiceMIMEAndKindFallback(t *testing.T) {
	for name, item := range map[string]attachment.Descriptor{
		"explicit voice MIME": {Kind: attachment.KindVoice, MIMEType: "audio/ogg"},
		"voice kind fallback": {Kind: attachment.KindVoice},
	} {
		t.Run(name, func(t *testing.T) {
			if got := extensionFromDescriptor(item); got != voiceBlobExtension {
				t.Fatalf("extensionFromDescriptor() = %q, want %s", got, voiceBlobExtension)
			}
		})
	}
}
