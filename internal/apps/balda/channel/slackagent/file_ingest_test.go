package slackagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/rs/zerolog"
)

type fileClientStub struct {
	resolveCalls  []string
	downloadCalls []string
	resolved      map[string]FileRef
	bodies        map[string]string
	resolveErr    error
	downloadErr   error
}

func (c *fileClientStub) ResolveFile(_ context.Context, file FileRef) (FileRef, error) {
	c.resolveCalls = append(c.resolveCalls, file.ID)
	if c.resolveErr != nil {
		return FileRef{}, c.resolveErr
	}
	if resolved, ok := c.resolved[file.ID]; ok {
		return resolved, nil
	}
	return file, nil
}

func (c *fileClientStub) DownloadFile(_ context.Context, file FileRef) (io.ReadCloser, error) {
	c.downloadCalls = append(c.downloadCalls, file.ID)
	if c.downloadErr != nil {
		return nil, c.downloadErr
	}
	return io.NopCloser(strings.NewReader(c.bodies[file.ID])), nil
}

type blobStoreStub struct {
	enabled  bool
	calls    []attachment.Descriptor
	maxBytes []int64
	failAt   int
	err      error
}

func (s *blobStoreStub) Enabled() bool { return s.enabled }

func (s *blobStoreStub) Persist(_ context.Context, descriptor attachment.Descriptor, body io.Reader, maxBytes int64) (attachment.Descriptor, error) {
	s.calls = append(s.calls, descriptor)
	s.maxBytes = append(s.maxBytes, maxBytes)
	if s.failAt > 0 && len(s.calls) == s.failAt {
		return attachment.Descriptor{}, s.err
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return attachment.Descriptor{}, err
	}
	if int64(len(data)) > maxBytes {
		return attachment.Descriptor{}, attachment.ErrTooLarge
	}
	descriptor.SizeBytes = int64(len(data))
	descriptor.Blob = &attachment.BlobRef{Store: "local", Key: descriptor.FileID, Path: "/state/attachments/" + descriptor.FileID}
	return descriptor, nil
}

func TestCurrentFileIngestorPersistsOrderedCompleteSet(t *testing.T) {
	t.Parallel()
	client := &fileClientStub{
		resolved: map[string]FileRef{
			"F1": {ID: "F1", Name: "photo.png", MIMEType: "image/png", SizeBytes: 3},
			"F2": {ID: "F2", Name: "report.pdf", MIMEType: "application/pdf", SizeBytes: 4},
		},
		bodies: map[string]string{"F1": "img", "F2": "data"},
	}
	blobs := &blobStoreStub{enabled: true}
	ingestor := NewCurrentFileIngestor(client, blobs, attachment.Limits{MaxFilesPerMessage: 10, MaxFileBytes: 25, MaxTotalBytes: 50}, zerolog.Nop())

	got, err := ingestor.Ingest(context.Background(), []FileRef{{ID: "F1"}, {ID: "F2"}})
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if len(got) != 2 || got[0].FileID != "F1" || got[1].FileID != "F2" {
		t.Fatalf("attachments = %+v, want ordered F1/F2", got)
	}
	if got[0].Kind != attachment.KindPhoto || got[1].Kind != attachment.KindDocument {
		t.Fatalf("attachment kinds = [%q %q]", got[0].Kind, got[1].Kind)
	}
	if got[0].Blob == nil || got[1].Blob == nil {
		t.Fatalf("attachments lack local blobs: %+v", got)
	}
	if strings.Join(client.resolveCalls, ",") != "F1,F2" || strings.Join(client.downloadCalls, ",") != "F1,F2" {
		t.Fatalf("resolve/download calls = %v/%v", client.resolveCalls, client.downloadCalls)
	}
}

func TestCurrentFileIngestorRejectsDeclaredLimitsBeforeIO(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		files  []FileRef
		reason string
	}{
		{name: "count", files: []FileRef{{ID: "F1"}, {ID: "F2"}, {ID: "F3"}}, reason: "too_many_files"},
		{name: "per file", files: []FileRef{{ID: "F1", SizeBytes: 11}}, reason: "file_size_exceeded"},
		{name: "aggregate", files: []FileRef{{ID: "F1", SizeBytes: 6}, {ID: "F2", SizeBytes: 5}}, reason: "total_size_exceeded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fileClientStub{}
			blobs := &blobStoreStub{enabled: true}
			ingestor := NewCurrentFileIngestor(client, blobs, attachment.Limits{MaxFilesPerMessage: 2, MaxFileBytes: 10, MaxTotalBytes: 10}, zerolog.Nop())

			got, err := ingestor.Ingest(context.Background(), test.files)
			if err == nil || fileFailureReason(err) != test.reason {
				t.Fatalf("Ingest() = %+v, %v; want reason %q", got, err, test.reason)
			}
			if IsRetryableSlackError(err) {
				t.Fatalf("limit error was retryable: %v", err)
			}
			if len(client.resolveCalls) != 0 || len(client.downloadCalls) != 0 || len(blobs.calls) != 0 {
				t.Fatalf("I/O occurred before limit rejection: resolve=%v download=%v persist=%d", client.resolveCalls, client.downloadCalls, len(blobs.calls))
			}
		})
	}
}

func TestCurrentFileIngestorEnforcesActualAggregateLimit(t *testing.T) {
	t.Parallel()
	client := &fileClientStub{
		bodies: map[string]string{"F1": "123456", "F2": "12345"},
	}
	blobs := &blobStoreStub{enabled: true}
	ingestor := NewCurrentFileIngestor(client, blobs, attachment.Limits{MaxFilesPerMessage: 2, MaxFileBytes: 10, MaxTotalBytes: 10}, zerolog.Nop())

	got, err := ingestor.Ingest(context.Background(), []FileRef{{ID: "F1"}, {ID: "F2"}})
	if err == nil || fileFailureReason(err) != "total_size_exceeded" {
		t.Fatalf("Ingest() = %+v, %v; want total_size_exceeded", got, err)
	}
	if len(got) != 0 {
		t.Fatalf("partial attachment set returned: %+v", got)
	}
	if len(blobs.calls) != 2 || blobs.maxBytes[1] != 4 {
		t.Fatalf("persist calls/max = %d/%v, want second bound 4", len(blobs.calls), blobs.maxBytes)
	}
}

func TestCurrentFileIngestorEnforcesActualPerFileLimit(t *testing.T) {
	t.Parallel()
	client := &fileClientStub{bodies: map[string]string{"F1": "12345678901"}}
	blobs := &blobStoreStub{enabled: true}
	ingestor := NewCurrentFileIngestor(client, blobs, attachment.Limits{MaxFilesPerMessage: 2, MaxFileBytes: 10, MaxTotalBytes: 20}, zerolog.Nop())

	got, err := ingestor.Ingest(context.Background(), []FileRef{{ID: "F1"}})
	if err == nil || fileFailureReason(err) != "file_size_exceeded" {
		t.Fatalf("Ingest() = %+v, %v; want file_size_exceeded", got, err)
	}
	if IsRetryableSlackError(err) {
		t.Fatalf("actual file limit error was retryable: %v", err)
	}
}

func TestCurrentFileIngestorClassifiesFailuresWithoutPartialResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		client        *fileClientStub
		blobs         *blobStoreStub
		wantReason    string
		wantRetryable bool
	}{
		{
			name:          "transient download",
			client:        &fileClientStub{downloadErr: &APIError{Method: "files.download", StatusCode: 429, Retryable: true}},
			blobs:         &blobStoreStub{enabled: true},
			wantReason:    "slack_rate_limited",
			wantRetryable: true,
		},
		{
			name:       "missing scope",
			client:     &fileClientStub{resolveErr: &APIError{Method: "files.info", StatusCode: 200, Code: "missing_scope"}},
			blobs:      &blobStoreStub{enabled: true},
			wantReason: "missing_scope",
		},
		{
			name:          "storage failure after first blob",
			client:        &fileClientStub{bodies: map[string]string{"F1": "one", "F2": "two"}},
			blobs:         &blobStoreStub{enabled: true, failAt: 2, err: errors.New("disk unavailable")},
			wantReason:    "storage_unavailable",
			wantRetryable: true,
		},
		{
			name:       "store disabled",
			client:     &fileClientStub{},
			blobs:      &blobStoreStub{enabled: false},
			wantReason: "attachment_store_disabled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ingestor := NewCurrentFileIngestor(test.client, test.blobs, attachment.Limits{MaxFilesPerMessage: 10, MaxFileBytes: 25, MaxTotalBytes: 50}, zerolog.Nop())
			files := []FileRef{{ID: "F1"}}
			if test.name == "storage failure after first blob" {
				files = append(files, FileRef{ID: "F2"})
			}

			got, err := ingestor.Ingest(context.Background(), files)
			if err == nil || fileFailureReason(err) != test.wantReason {
				t.Fatalf("Ingest() = %+v, %v; want reason %q", got, err, test.wantReason)
			}
			if IsRetryableSlackError(err) != test.wantRetryable {
				t.Fatalf("retryable = %v, want %v for %v", IsRetryableSlackError(err), test.wantRetryable, err)
			}
			if len(got) != 0 {
				t.Fatalf("partial attachment set returned: %+v", got)
			}
		})
	}
}

func TestCurrentFileIngestorLogsSafeBoundedDiagnostics(t *testing.T) {
	const (
		privateURL = "https://files.slack.com/files-pri/private-url-secret"
		rawBody    = "raw-body-secret"
		prompt     = "prompt-content-secret"
		token      = "xoxb-token-secret"
	)
	limits := attachment.Limits{MaxFilesPerMessage: 10, MaxFileBytes: 100, MaxTotalBytes: 200}

	t.Run("accepted", func(t *testing.T) {
		var logs bytes.Buffer
		file := FileRef{
			ID:                 "F1",
			Title:              prompt,
			MIMEType:           "image/private-mime-secret",
			SizeBytes:          int64(len(rawBody)),
			privateDownloadURL: privateURL,
		}
		ingestor := NewCurrentFileIngestor(
			&fileClientStub{bodies: map[string]string{"F1": rawBody}},
			&blobStoreStub{enabled: true},
			limits,
			zerolog.New(&logs),
		)

		if _, err := ingestor.Ingest(context.Background(), []FileRef{file}); err != nil {
			t.Fatalf("Ingest() error = %v", err)
		}
		entry := decodeFileIngestLog(t, logs.Bytes())
		assertFileIngestLogFields(t, entry, "accepted", "image", "persistence", "accepted")
		if got, ok := entry["file_ids"].([]any); !ok || len(got) != 1 || got[0] != "F1" {
			t.Fatalf("file_ids = %#v, want [F1]", entry["file_ids"])
		}
		assertLogOmits(t, logs.String(), privateURL, rawBody, prompt, token, "private-mime-secret")
	})

	t.Run("retry", func(t *testing.T) {
		var logs bytes.Buffer
		file := FileRef{ID: "F2", Title: prompt, MIMEType: "application/private-mime-secret", privateURL: privateURL}
		cause := errors.New("Bearer " + token + " Authorization " + privateURL + " " + rawBody + " " + prompt)
		ingestor := NewCurrentFileIngestor(
			&fileClientStub{bodies: map[string]string{"F2": rawBody}},
			&blobStoreStub{enabled: true, failAt: 1, err: cause},
			limits,
			zerolog.New(&logs),
		)

		if _, err := ingestor.Ingest(context.Background(), []FileRef{file}); err == nil {
			t.Fatal("Ingest() error = nil, want retryable storage failure")
		}
		entry := decodeFileIngestLog(t, logs.Bytes())
		assertFileIngestLogFields(t, entry, "retry", "application", "persistence", "storage_unavailable")
		if entry["file_id"] != "F2" {
			t.Fatalf("file_id = %#v, want F2", entry["file_id"])
		}
		assertLogOmits(t, logs.String(), privateURL, rawBody, prompt, token, "Authorization", "private-mime-secret")
	})

	t.Run("terminal", func(t *testing.T) {
		var logs bytes.Buffer
		file := FileRef{ID: "F3", Title: prompt, MIMEType: "video/private-mime-secret", SizeBytes: limits.MaxFileBytes + 1, privateURL: privateURL}
		ingestor := NewCurrentFileIngestor(
			&fileClientStub{},
			&blobStoreStub{enabled: true},
			limits,
			zerolog.New(&logs),
		)

		if _, err := ingestor.Ingest(context.Background(), []FileRef{file}); err == nil {
			t.Fatal("Ingest() error = nil, want terminal limit failure")
		}
		entry := decodeFileIngestLog(t, logs.Bytes())
		assertFileIngestLogFields(t, entry, "terminal", "video", "preflight", "file_size_exceeded")
		if entry["file_id"] != "F3" {
			t.Fatalf("file_id = %#v, want F3", entry["file_id"])
		}
		assertLogOmits(t, logs.String(), privateURL, prompt, token, "private-mime-secret")
	})
}

func decodeFileIngestLog(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("decode log %q: %v", data, err)
	}
	return entry
}

func assertFileIngestLogFields(t *testing.T, entry map[string]any, settlement, mimeClass, stage, reason string) {
	t.Helper()
	for field, want := range map[string]string{
		"settlement_class": settlement,
		"mime_class":       mimeClass,
		"stage":            stage,
		"reason":           reason,
	} {
		if got := entry[field]; got != want {
			t.Errorf("%s = %#v, want %q", field, got, want)
		}
	}
	if _, ok := entry["file_count"]; !ok {
		t.Error("log lacks file_count")
	}
	if _, ok := entry["declared_bytes"]; !ok {
		t.Error("log lacks declared_bytes")
	}
}

func assertLogOmits(t *testing.T, log string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(log, value) {
			t.Errorf("log contains forbidden sentinel %q: %s", value, log)
		}
	}
}
