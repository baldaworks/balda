package slackagent

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/rs/zerolog"
)

func TestHistoricalContextHydratorPrioritizesCurrentAndOrdersSelectedHistory(t *testing.T) {
	t.Parallel()
	client := &fileClientStub{bodies: map[string]string{"F1": "11", "F2": "222", "F3": "333"}}
	blobs := &blobStoreStub{enabled: true}
	hydrator := NewHistoricalContextHydrator(
		client,
		blobs,
		attachment.Limits{MaxFilesPerMessage: 3, MaxFileBytes: 8, MaxTotalBytes: 8},
		zerolog.Nop(),
	)
	snapshot := historicalSnapshot(
		ThreadMessage{TS: "100.1", AuthorType: ThreadAuthorHuman, Text: "root", files: []FileRef{{ID: "F1", MIMEType: "text/plain", SizeBytes: 2}}},
		ThreadMessage{TS: "100.2", AuthorType: ThreadAuthorHuman, Text: "middle", files: []FileRef{{ID: "F2", MIMEType: "application/pdf", SizeBytes: 3}}},
		ThreadMessage{TS: "100.3", AuthorType: ThreadAuthorHuman, Text: "newest", files: []FileRef{{ID: "F3", MIMEType: "image/png", SizeBytes: 3}}},
	)
	current := []attachment.Descriptor{{Kind: attachment.KindDocument, FileID: "CURRENT", SizeBytes: 2, Blob: &attachment.BlobRef{Path: "/state/current"}}}

	result, err := hydrator.Hydrate(context.Background(), snapshot, "help", current)
	if err != nil {
		t.Fatalf("Hydrate() error = %v", err)
	}
	if got := descriptorIDs(result.Attachments); strings.Join(got, ",") != "F2,F3" {
		t.Fatalf("historical attachment order = %v, want F2,F3", got)
	}
	if got := strings.Join(client.resolveCalls, ","); got != "F3,F2" {
		t.Fatalf("resolve order = %q, want newest-first F3,F2", got)
	}
	if got := strings.Join(client.downloadCalls, ","); got != "F3,F2" {
		t.Fatalf("download order = %q, want newest-first F3,F2", got)
	}
	statuses := historicalMarkerStatuses(t, result.Prompt)
	if got := strings.Join(statuses, ","); got != "over_budget,supplied,supplied" {
		t.Fatalf("marker statuses = %q", got)
	}
}

func TestHistoricalContextHydratorDeduplicatesBySlackFileID(t *testing.T) {
	t.Parallel()
	client := &fileClientStub{bodies: map[string]string{"F1": "data"}}
	hydrator := NewHistoricalContextHydrator(
		client,
		&blobStoreStub{enabled: true},
		attachment.Limits{MaxFilesPerMessage: 10, MaxFileBytes: 10, MaxTotalBytes: 20},
		zerolog.Nop(),
	)
	snapshot := historicalSnapshot(
		ThreadMessage{TS: "100.1", AuthorType: ThreadAuthorHuman, Text: "root", files: []FileRef{{ID: "F1", Name: "old.txt"}}},
		ThreadMessage{TS: "100.2", AuthorType: ThreadAuthorHuman, Text: "again", files: []FileRef{{ID: "F1", Name: "new.txt"}}},
	)

	result, err := hydrator.Hydrate(context.Background(), snapshot, "help", nil)
	if err != nil {
		t.Fatalf("Hydrate() error = %v", err)
	}
	if len(client.resolveCalls) != 1 || len(client.downloadCalls) != 1 || len(result.Attachments) != 1 {
		t.Fatalf("resolve/download/attachments = %v/%v/%+v", client.resolveCalls, client.downloadCalls, result.Attachments)
	}
	statuses := historicalMarkerStatuses(t, result.Prompt)
	if got := strings.Join(statuses, ","); got != "duplicate,supplied" {
		t.Fatalf("marker statuses = %q", got)
	}
	payload := decodeThreadContextPayload(t, result.Prompt)
	if payload.Messages[0].Files[0].Reference != payload.Messages[1].Files[0].Reference {
		t.Fatalf("duplicate references differ: %+v", payload.Messages)
	}
}

func TestHistoricalContextHydratorContinuesAfterPermanentBudgetAndAccessOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		file       FileRef
		client     *fileClientStub
		blobs      *blobStoreStub
		wantStatus historicalFileStatus
		wantReason string
		wantIO     bool
	}{
		{
			name:       "declared over budget",
			file:       FileRef{ID: "F1", SizeBytes: 11},
			client:     &fileClientStub{},
			blobs:      &blobStoreStub{enabled: true},
			wantStatus: historicalFileOverBudget,
			wantReason: "file_size_exceeded",
		},
		{
			name:       "unsupported mode",
			file:       FileRef{ID: "F1", Mode: "external"},
			client:     &fileClientStub{},
			blobs:      &blobStoreStub{enabled: true},
			wantStatus: historicalFileUnsupported,
			wantReason: "unsupported_file_mode",
		},
		{
			name:       "missing scope",
			file:       FileRef{ID: "F1"},
			client:     &fileClientStub{resolveErr: &APIError{Method: "files.info", StatusCode: 200, Code: "missing_scope"}},
			blobs:      &blobStoreStub{enabled: true},
			wantStatus: historicalFileUnavailable,
			wantReason: "missing_scope",
			wantIO:     true,
		},
		{
			name:       "store disabled",
			file:       FileRef{ID: "F1"},
			client:     &fileClientStub{},
			blobs:      &blobStoreStub{enabled: false},
			wantStatus: historicalFileUnavailable,
			wantReason: "attachment_store_disabled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hydrator := NewHistoricalContextHydrator(
				test.client,
				test.blobs,
				attachment.Limits{MaxFilesPerMessage: 10, MaxFileBytes: 10, MaxTotalBytes: 20},
				zerolog.Nop(),
			)
			result, err := hydrator.Hydrate(context.Background(), historicalSnapshot(
				ThreadMessage{TS: "100.1", AuthorType: ThreadAuthorHuman, Text: "root", files: []FileRef{test.file}},
			), "help", nil)
			if err != nil {
				t.Fatalf("Hydrate() error = %v", err)
			}
			if len(result.Attachments) != 0 {
				t.Fatalf("attachments = %+v", result.Attachments)
			}
			payload := decodeThreadContextPayload(t, result.Prompt)
			marker := payload.Messages[0].Files[0]
			if marker.Status != test.wantStatus || marker.Reason != test.wantReason {
				t.Fatalf("marker = %+v, want %q/%q", marker, test.wantStatus, test.wantReason)
			}
			if gotIO := len(test.client.resolveCalls) > 0; gotIO != test.wantIO {
				t.Fatalf("metadata I/O = %v, want %v", gotIO, test.wantIO)
			}
		})
	}
}

func TestHistoricalContextHydratorSkipsActualOverflowAndAdmitsOlderFile(t *testing.T) {
	t.Parallel()
	client := &fileClientStub{bodies: map[string]string{"OLD": "ok", "NEW": "123456"}}
	blobs := &blobStoreStub{enabled: true}
	hydrator := NewHistoricalContextHydrator(
		client,
		blobs,
		attachment.Limits{MaxFilesPerMessage: 2, MaxFileBytes: 5, MaxTotalBytes: 5},
		zerolog.Nop(),
	)
	result, err := hydrator.Hydrate(context.Background(), historicalSnapshot(
		ThreadMessage{TS: "100.1", AuthorType: ThreadAuthorHuman, Text: "root", files: []FileRef{{ID: "OLD"}}},
		ThreadMessage{TS: "100.2", AuthorType: ThreadAuthorHuman, Text: "new", files: []FileRef{{ID: "NEW"}}},
	), "help", nil)
	if err != nil {
		t.Fatalf("Hydrate() error = %v", err)
	}
	if got := descriptorIDs(result.Attachments); strings.Join(got, ",") != "OLD" {
		t.Fatalf("attachments = %v, want OLD", got)
	}
	if got := strings.Join(client.downloadCalls, ","); got != "NEW,OLD" {
		t.Fatalf("download order = %q", got)
	}
	if got := strings.Join(historicalMarkerStatuses(t, result.Prompt), ","); got != "supplied,over_budget" {
		t.Fatalf("marker statuses = %q", got)
	}
}

func TestHistoricalContextHydratorReturnsNoPartialResultOnTransientFailure(t *testing.T) {
	t.Parallel()
	client := &fileClientStub{resolveErr: &APIError{Method: "files.info", StatusCode: 429, Retryable: true}}
	hydrator := NewHistoricalContextHydrator(
		client,
		&blobStoreStub{enabled: true},
		attachment.Limits{MaxFilesPerMessage: 10, MaxFileBytes: 10, MaxTotalBytes: 20},
		zerolog.Nop(),
	)
	result, err := hydrator.Hydrate(context.Background(), historicalSnapshot(
		ThreadMessage{TS: "100.1", AuthorType: ThreadAuthorHuman, Text: "root", files: []FileRef{{ID: "F1"}}},
	), "help", nil)
	if err == nil || !IsRetryableSlackError(err) {
		t.Fatalf("Hydrate() = %+v, %v; want retryable error", result, err)
	}
	if result.Prompt != "" || len(result.Attachments) != 0 {
		t.Fatalf("partial result = %+v", result)
	}
}

func TestHistoricalContextHydratorRejectsInvalidCurrentBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		current attachment.Descriptor
	}{
		{name: "negative size", current: attachment.Descriptor{Kind: attachment.KindDocument, FileID: "bad", SizeBytes: -1, Blob: &attachment.BlobRef{Path: "/state/bad"}}},
		{name: "missing blob", current: attachment.Descriptor{Kind: attachment.KindDocument, FileID: "bad", SizeBytes: 1}},
		{name: "per-file overflow", current: attachment.Descriptor{Kind: attachment.KindDocument, FileID: "bad", SizeBytes: 11, Blob: &attachment.BlobRef{Path: "/state/bad"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fileClientStub{}
			hydrator := NewHistoricalContextHydrator(
				client,
				&blobStoreStub{enabled: true},
				attachment.Limits{MaxFilesPerMessage: 1, MaxFileBytes: 10, MaxTotalBytes: 20},
				zerolog.Nop(),
			)
			result, err := hydrator.Hydrate(context.Background(), historicalSnapshot(
				ThreadMessage{TS: "100.1", AuthorType: ThreadAuthorHuman, files: []FileRef{{ID: "F1"}}},
			), "help", []attachment.Descriptor{test.current})
			if err == nil || !IsRetryableSlackError(err) || fileFailureReason(err) != "invalid_current_attachment_budget" {
				t.Fatalf("Hydrate() = %+v, %v", result, err)
			}
			if len(client.resolveCalls) != 0 {
				t.Fatalf("metadata calls = %v", client.resolveCalls)
			}
		})
	}
}

func TestHistoricalContextHydratorLogsOnlySafeFields(t *testing.T) {
	const secret = "Bearer xoxb-secret https://files.slack.com/private raw-body prompt-content private-mime"
	var logs bytes.Buffer
	client := &fileClientStub{resolveErr: newFileError("F1", "missing_scope", false, errors.New(secret))}
	hydrator := NewHistoricalContextHydrator(
		client,
		&blobStoreStub{enabled: true},
		attachment.Limits{MaxFilesPerMessage: 10, MaxFileBytes: 100, MaxTotalBytes: 200},
		zerolog.New(&logs),
	)
	result, err := hydrator.Hydrate(context.Background(), historicalSnapshot(
		ThreadMessage{TS: "100.1", AuthorType: ThreadAuthorHuman, Text: "root", files: []FileRef{{ID: "F1", Title: secret, MIMEType: "image/private-mime"}}},
	), "prompt-content", nil)
	if err != nil {
		t.Fatalf("Hydrate() error = %v", err)
	}
	if len(result.Attachments) != 0 {
		t.Fatalf("attachments = %+v", result.Attachments)
	}
	assertLogOmits(t, logs.String(), secret, "xoxb-secret", "files.slack.com", "raw-body", "prompt-content", "private-mime")
	if !strings.Contains(logs.String(), `"settlement_class":"terminal"`) || !strings.Contains(logs.String(), `"file_id":"F1"`) {
		t.Fatalf("safe diagnostic fields absent: %s", logs.String())
	}
}

func historicalSnapshot(messages ...ThreadMessage) ThreadSnapshot {
	return ThreadSnapshot{
		RootTS:    messages[0].TS,
		CutoffTS:  "101.0",
		Messages:  messages,
		Available: true,
	}
}

func historicalMarkerStatuses(t *testing.T, prompt string) []string {
	t.Helper()
	payload := decodeThreadContextPayload(t, prompt)
	var statuses []string
	for _, message := range payload.Messages {
		for _, marker := range message.Files {
			statuses = append(statuses, string(marker.Status))
		}
	}
	return statuses
}

func descriptorIDs(descriptors []attachment.Descriptor) []string {
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		ids = append(ids, descriptor.FileID)
	}
	return ids
}
