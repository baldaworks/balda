package telegram

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/rs/zerolog"
)

type recordingBlobStore struct {
	enabled    bool
	descriptor attachment.Descriptor
	body       string
	maxBytes   int64
	maxValues  []int64
	enforceMax bool
	err        error
}

func (s *recordingBlobStore) Enabled() bool { return s.enabled }

func (s *recordingBlobStore) Persist(_ context.Context, descriptor attachment.Descriptor, body io.Reader, maxBytes int64) (attachment.Descriptor, error) {
	s.descriptor = descriptor
	s.maxBytes = maxBytes
	s.maxValues = append(s.maxValues, maxBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		return attachment.Descriptor{}, err
	}
	s.body = string(data)
	if s.enforceMax && int64(len(data)) > maxBytes {
		return attachment.Descriptor{}, attachment.ErrTooLarge
	}
	if s.err != nil {
		return attachment.Descriptor{}, s.err
	}
	descriptor.SizeBytes = int64(len(data))
	descriptor.Blob = &attachment.BlobRef{Store: "local", Key: "blob-key", Path: "/state/attachments/blob-key"}
	return descriptor, nil
}

type recordingTelegramFileDownloader struct {
	fileIDs []string
	body    *trackingReadCloser
	bodies  []io.ReadCloser
	err     error
}

func (d *recordingTelegramFileDownloader) DownloadFile(_ context.Context, fileID string) (io.ReadCloser, error) {
	d.fileIDs = append(d.fileIDs, fileID)
	if d.err != nil {
		return nil, d.err
	}
	if len(d.bodies) >= len(d.fileIDs) {
		return d.bodies[len(d.fileIDs)-1], nil
	}
	return d.body, nil
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func testTelegramAttachmentLimits() attachment.Limits {
	return attachment.Limits{MaxFilesPerMessage: 10, MaxFileBytes: 32, MaxTotalBytes: 64}
}

func TestAttachmentStoreDownloadsAndPersistsTelegramFile(t *testing.T) {
	blobs := &recordingBlobStore{enabled: true}
	body := &trackingReadCloser{Reader: strings.NewReader("telegram body")}
	downloader := &recordingTelegramFileDownloader{body: body}
	store := &attachmentStore{blobs: blobs, downloader: downloader, limits: testTelegramAttachmentLimits()}
	descriptor := attachment.Descriptor{
		Kind:      attachment.KindDocument,
		FileID:    "telegram-file",
		FileName:  "report.pdf",
		MIMEType:  "application/pdf",
		SizeBytes: 123,
	}

	got, err := store.PersistTelegram(context.Background(), []attachment.Descriptor{descriptor})
	if err != nil {
		t.Fatalf("PersistTelegram() error = %v", err)
	}
	if len(got) != 1 || got[0].Blob == nil {
		t.Fatalf("persisted descriptors = %+v, want one blob-backed descriptor", got)
	}
	if len(downloader.fileIDs) != 1 || downloader.fileIDs[0] != descriptor.FileID {
		t.Fatalf("downloaded file IDs = %v, want [%s]", downloader.fileIDs, descriptor.FileID)
	}
	if blobs.descriptor != descriptor {
		t.Fatalf("persisted descriptor = %+v, want %+v", blobs.descriptor, descriptor)
	}
	if blobs.body != "telegram body" {
		t.Fatalf("persisted body = %q, want telegram body", blobs.body)
	}
	if blobs.maxBytes != testTelegramAttachmentLimits().MaxFileBytes {
		t.Fatalf("max bytes = %d, want %d", blobs.maxBytes, testTelegramAttachmentLimits().MaxFileBytes)
	}
	if !body.closed {
		t.Fatal("downloaded body was not closed")
	}
}

func TestAttachmentStoreOffPreservesTelegramDescriptors(t *testing.T) {
	blobs := &recordingBlobStore{enabled: false}
	downloader := &recordingTelegramFileDownloader{body: &trackingReadCloser{Reader: strings.NewReader("unused")}}
	store := &attachmentStore{blobs: blobs, downloader: downloader, limits: testTelegramAttachmentLimits()}
	descriptor := attachment.Descriptor{Kind: attachment.KindPhoto, FileID: "photo"}

	got, err := store.PersistTelegram(context.Background(), []attachment.Descriptor{descriptor})
	if err != nil {
		t.Fatalf("PersistTelegram() error = %v", err)
	}
	if len(got) != 1 || got[0].FileID != descriptor.FileID || got[0].Blob != nil {
		t.Fatalf("persisted descriptors = %+v, want unchanged metadata", got)
	}
	if len(downloader.fileIDs) != 0 {
		t.Fatalf("downloaded file IDs = %v, want none", downloader.fileIDs)
	}
}

func TestAttachmentStoreClosesBodyAfterPersistenceFailure(t *testing.T) {
	persistErr := errors.New("storage unavailable")
	blobs := &recordingBlobStore{enabled: true, err: persistErr}
	body := &trackingReadCloser{Reader: strings.NewReader("body")}
	store := &attachmentStore{
		blobs:      blobs,
		downloader: &recordingTelegramFileDownloader{body: body},
		limits:     testTelegramAttachmentLimits(),
	}

	_, err := store.PersistTelegram(context.Background(), []attachment.Descriptor{{Kind: attachment.KindDocument, FileID: "file"}})
	if !errors.Is(err, persistErr) {
		t.Fatalf("PersistTelegram() error = %v, want %v", err, persistErr)
	}
	if !body.closed {
		t.Fatal("downloaded body was not closed after persistence failure")
	}
}

func TestAttachmentStoreRejectsTooManyFilesBeforeDownload(t *testing.T) {
	downloader := &recordingTelegramFileDownloader{body: &trackingReadCloser{Reader: strings.NewReader("unused")}}
	store := &attachmentStore{
		blobs:      &recordingBlobStore{enabled: true},
		downloader: downloader,
		limits:     attachment.Limits{MaxFilesPerMessage: 1, MaxFileBytes: 10, MaxTotalBytes: 20},
	}
	descriptors := []attachment.Descriptor{
		{Kind: attachment.KindDocument, FileID: "first"},
		{Kind: attachment.KindDocument, FileID: "second"},
	}

	_, err := store.PersistTelegram(context.Background(), descriptors)
	if !errors.Is(err, attachment.ErrTooLarge) {
		t.Fatalf("PersistTelegram() error = %v, want ErrTooLarge", err)
	}
	if len(downloader.fileIDs) != 0 {
		t.Fatalf("downloaded file IDs = %v, want none", downloader.fileIDs)
	}
}

func TestAttachmentStoreAppliesRemainingAggregateLimit(t *testing.T) {
	blobs := &recordingBlobStore{enabled: true}
	downloader := &recordingTelegramFileDownloader{bodies: []io.ReadCloser{
		&trackingReadCloser{Reader: strings.NewReader("123456")},
		&trackingReadCloser{Reader: strings.NewReader("7890")},
	}}
	store := &attachmentStore{
		blobs:      blobs,
		downloader: downloader,
		limits:     attachment.Limits{MaxFilesPerMessage: 2, MaxFileBytes: 8, MaxTotalBytes: 10},
	}
	descriptors := []attachment.Descriptor{
		{Kind: attachment.KindDocument, FileID: "first"},
		{Kind: attachment.KindDocument, FileID: "second"},
	}

	got, err := store.PersistTelegram(context.Background(), descriptors)
	if err != nil {
		t.Fatalf("PersistTelegram() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("persisted descriptors = %d, want 2", len(got))
	}
	if len(blobs.maxValues) != 2 || blobs.maxValues[0] != 8 || blobs.maxValues[1] != 4 {
		t.Fatalf("persistence limits = %v, want [8 4]", blobs.maxValues)
	}
}

func TestAttachmentStoreRejectsAggregateOverflow(t *testing.T) {
	blobs := &recordingBlobStore{enabled: true, enforceMax: true}
	firstBody := &trackingReadCloser{Reader: strings.NewReader("123456")}
	secondBody := &trackingReadCloser{Reader: strings.NewReader("78901")}
	store := &attachmentStore{
		blobs: blobs,
		downloader: &recordingTelegramFileDownloader{bodies: []io.ReadCloser{
			firstBody,
			secondBody,
		}},
		limits: attachment.Limits{MaxFilesPerMessage: 2, MaxFileBytes: 8, MaxTotalBytes: 10},
	}
	descriptors := []attachment.Descriptor{
		{Kind: attachment.KindDocument, FileID: "first"},
		{Kind: attachment.KindDocument, FileID: "second"},
	}

	_, err := store.PersistTelegram(context.Background(), descriptors)
	if !errors.Is(err, attachment.ErrTooLarge) {
		t.Fatalf("PersistTelegram() error = %v, want ErrTooLarge", err)
	}
	if len(blobs.maxValues) != 2 || blobs.maxValues[1] != 4 {
		t.Fatalf("persistence limits = %v, want second limit 4", blobs.maxValues)
	}
	if !firstBody.closed || !secondBody.closed {
		t.Fatalf("body closure = (%v, %v), want both closed", firstBody.closed, secondBody.closed)
	}
}

func TestAttachmentStoreRejectsInvalidLimits(t *testing.T) {
	downloader := &recordingTelegramFileDownloader{body: &trackingReadCloser{Reader: strings.NewReader("unused")}}
	store := &attachmentStore{blobs: &recordingBlobStore{enabled: true}, downloader: downloader}

	_, err := store.PersistTelegram(context.Background(), []attachment.Descriptor{{Kind: attachment.KindDocument, FileID: "file"}})
	if err == nil {
		t.Fatal("PersistTelegram() error = nil, want invalid limits error")
	}
	if len(downloader.fileIDs) != 0 {
		t.Fatalf("downloaded file IDs = %v, want none", downloader.fileIDs)
	}
}

type failingAttachmentStore struct {
	err error
}

func (s failingAttachmentStore) PersistTelegram(context.Context, []attachment.Descriptor) ([]attachment.Descriptor, error) {
	return nil, s.err
}

type attachmentRecordingInbound struct {
	messages []MessageContext
}

func (h *attachmentRecordingInbound) HandleMessage(_ context.Context, message MessageContext) error {
	h.messages = append(h.messages, message)
	return nil
}

func (*attachmentRecordingInbound) HandleCallback(context.Context, CallbackContext) error { return nil }
func (*attachmentRecordingInbound) HandleForumTopic(context.Context, TopicLifecycleContext) error {
	return nil
}

func TestServerPreservesTelegramMetadataWhenPersistenceFails(t *testing.T) {
	persistErr := errors.New("temporary storage failure")
	inbound := &attachmentRecordingInbound{}
	server := &Server{
		attachmentStore: failingAttachmentStore{err: persistErr},
		inboundHandler:  inbound,
		logger:          zerolog.Nop(),
	}
	descriptor := attachment.Descriptor{Kind: attachment.KindDocument, FileID: "file"}

	if err := server.handleAcceptedMessage(context.Background(), MessageContext{Attachments: []attachment.Descriptor{descriptor}}); err != nil {
		t.Fatalf("handleAcceptedMessage() error = %v", err)
	}
	if len(inbound.messages) != 1 || len(inbound.messages[0].Attachments) != 1 {
		t.Fatalf("inbound messages = %+v, want original attachment metadata", inbound.messages)
	}
	if got := inbound.messages[0].Attachments[0]; got.FileID != descriptor.FileID || got.Blob != nil {
		t.Fatalf("inbound attachment = %+v, want original descriptor", got)
	}
}
