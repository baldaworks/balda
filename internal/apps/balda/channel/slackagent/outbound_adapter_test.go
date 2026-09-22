package slackagent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/rs/zerolog"
)

const testOutboundFileID = "F123RESULT"

func TestAdapterDeliversLocalMedia(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		kind     deliverycmd.OperationKind
		fileName string
		mimeType string
		content  string
	}{
		{name: "photo", kind: deliverycmd.OperationPhoto, fileName: "result.png", mimeType: "image/png", content: "png bytes"},
		{name: "document", kind: deliverycmd.OperationDocument, fileName: "report.txt", mimeType: "text/plain", content: "report bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), test.fileName)
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			uploader := &recordingMediaUploader{fileID: testOutboundFileID}
			adapter := NewAdapter(nil, uploader, attachment.Limits{MaxFileBytes: 1024}, zerolog.Nop(), AdapterConfig{})
			result, err := adapter.Deliver(t.Context(), NewThreadLocator("T123", "C456", "171.25"), deliverycmd.Operation{
				Kind: test.kind,
				Media: &deliverycmd.Media{
					LocalPath: path,
					Name:      test.fileName,
					MIMEType:  test.mimeType + "; charset=binary",
					Caption:   " generated result ",
				},
			})
			if err != nil {
				t.Fatalf("Deliver() error = %v", err)
			}
			if result.ProviderMessageID != testOutboundFileID {
				t.Fatalf("provider_message_id = %q", result.ProviderMessageID)
			}
			if uploader.calls != 1 {
				t.Fatalf("upload calls = %d, want 1", uploader.calls)
			}
			got := uploader.request
			if got.ChannelID != "C456" || got.ThreadTS != "171.25" || got.FileName != test.fileName || got.MIMEType != test.mimeType || got.Caption != "generated result" {
				t.Fatalf("upload request = %+v", got)
			}
			if got.SizeBytes != int64(len(test.content)) || uploader.body != test.content {
				t.Fatalf("upload size/body = %d/%q", got.SizeBytes, uploader.body)
			}
		})
	}
}

func TestAdapterRejectsInvalidMediaBeforeUpload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.txt")
	if err := os.WriteFile(validPath, []byte("valid"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	emptyPath := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	largePath := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(largePath, []byte("too large"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	symlinkPath := filepath.Join(dir, "link.txt")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	validLocator := NewThreadLocator("T123", "C456", "171.25")
	tests := []struct {
		name    string
		locator deliverycmd.Locator
		media   *deliverycmd.Media
		limit   int64
	}{
		{name: "missing media", locator: validLocator, limit: 100},
		{name: "file ID only", locator: validLocator, media: &deliverycmd.Media{FileID: "telegram-file-id"}, limit: 100},
		{name: "URL", locator: validLocator, media: &deliverycmd.Media{LocalPath: "https://example.com/private"}, limit: 100},
		{name: "missing path", locator: validLocator, media: &deliverycmd.Media{LocalPath: filepath.Join(dir, "missing")}, limit: 100},
		{name: "directory", locator: validLocator, media: &deliverycmd.Media{LocalPath: dir}, limit: 100},
		{name: "symlink", locator: validLocator, media: &deliverycmd.Media{LocalPath: symlinkPath}, limit: 100},
		{name: "empty file", locator: validLocator, media: &deliverycmd.Media{LocalPath: emptyPath}, limit: 100},
		{name: "oversized", locator: validLocator, media: &deliverycmd.Media{LocalPath: largePath}, limit: 2},
		{name: "invalid limits", locator: validLocator, media: &deliverycmd.Media{LocalPath: validPath}},
		{name: "wrong transport", locator: deliverycmd.Locator{ChannelType: "telegram"}, media: &deliverycmd.Media{LocalPath: validPath}, limit: 100},
		{name: "missing thread", locator: NewConversationLocator("T123", "C456"), media: &deliverycmd.Media{LocalPath: validPath}, limit: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			uploader := &recordingMediaUploader{fileID: "F123"}
			adapter := NewAdapter(nil, uploader, attachment.Limits{MaxFileBytes: test.limit}, zerolog.Nop(), AdapterConfig{})
			_, err := adapter.Deliver(t.Context(), test.locator, deliverycmd.Operation{Kind: deliverycmd.OperationDocument, Media: test.media})
			if kind, ok := deliverycmd.ClassifyError(err); !ok || kind != deliverycmd.ErrorKindPermanent {
				t.Fatalf("Deliver() error kind = %q, want permanent (err: %v)", kind, err)
			}
			if uploader.calls != 0 {
				t.Fatalf("upload calls = %d, want 0", uploader.calls)
			}
		})
	}
}

func TestAdapterMediaSourceValidationAndSafeLogging(t *testing.T) {
	t.Parallel()
	const (
		privateCaption = "private caption sentinel"
		privateContent = "private content sentinel"
	)
	path := filepath.Join(t.TempDir(), "private-path-sentinel.txt")
	if err := os.WriteFile(path, []byte(privateContent), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	uploader := &recordingMediaUploader{beforeValidation: func() error {
		if err := os.WriteFile(path, []byte("changed content sentinel"), 0o600); err != nil {
			return err
		}
		return os.Chtimes(path, time.Now(), time.Now().Add(time.Hour))
	}}
	var logs bytes.Buffer
	adapter := NewAdapter(nil, uploader, attachment.Limits{MaxFileBytes: 1024}, zerolog.New(&logs), AdapterConfig{})
	_, err := adapter.Deliver(t.Context(), NewThreadLocator("T123", "C456", "171.25"), deliverycmd.Operation{
		Kind: deliverycmd.OperationDocument,
		Media: &deliverycmd.Media{
			LocalPath: path,
			Caption:   privateCaption,
			MIMEType:  "text/plain",
		},
	})
	if err == nil || !errors.Is(err, errSourceChanged) {
		t.Fatalf("Deliver() error = %v, want source change", err)
	}
	for _, private := range []string{path, privateCaption, privateContent} {
		if strings.Contains(logs.String(), private) || strings.Contains(err.Error(), private) {
			t.Fatalf("media diagnostics leaked %q: error=%v logs=%s", private, err, logs.String())
		}
	}
}

var errSourceChanged = errors.New("source changed")

type recordingMediaUploader struct {
	fileID           string
	beforeValidation func() error
	request          UploadFileRequest
	body             string
	calls            int
}

func (u *recordingMediaUploader) UploadFile(_ context.Context, request UploadFileRequest) (string, error) {
	u.calls++
	u.request = request
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return "", err
	}
	u.body = string(body)
	if u.beforeValidation != nil {
		if err := u.beforeValidation(); err != nil {
			return "", err
		}
	}
	if request.ValidateSource != nil {
		if err := request.ValidateSource(); err != nil {
			return "", errSourceChanged
		}
	}
	return u.fileID, nil
}
