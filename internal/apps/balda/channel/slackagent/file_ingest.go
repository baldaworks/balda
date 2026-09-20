package slackagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/rs/zerolog"
)

const (
	mediaUnavailableReason  = "media_unavailable"
	unknownDiagnosticValue  = "unknown"
	fileSizeExceededReason  = "file_size_exceeded"
	totalSizeExceededReason = "total_size_exceeded"
	checkFileInfoAccess     = "check_file_info"
)

// BlobStore is the persistence capability consumed by Slack file ingestion.
type BlobStore interface {
	Enabled() bool
	Persist(ctx context.Context, descriptor attachment.Descriptor, body io.Reader, maxBytes int64) (attachment.Descriptor, error)
}

// CurrentFileIngestor persists every file on one triggering Slack event.
type CurrentFileIngestor interface {
	Ingest(ctx context.Context, files []FileRef) ([]attachment.Descriptor, error)
}

type currentFileIngestor struct {
	client FileClient
	blobs  BlobStore
	limits attachment.Limits
	logger zerolog.Logger
}

// NewCurrentFileIngestor constructs the Slack current-message file pipeline.
func NewCurrentFileIngestor(client FileClient, blobs BlobStore, limits attachment.Limits, logger zerolog.Logger) CurrentFileIngestor {
	return &currentFileIngestor{
		client: client,
		blobs:  blobs,
		limits: limits,
		logger: logger.With().Str("component", "balda.channel.slackagent.files").Logger(),
	}
}

func (s *currentFileIngestor) Ingest(ctx context.Context, files []FileRef) ([]attachment.Descriptor, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if s == nil {
		return nil, newFileError("", "media_ingestor_unavailable", true, nil)
	}
	if s.client == nil || s.blobs == nil {
		err := newFileError("", "media_ingestor_unavailable", true, nil)
		s.logFailure("wiring", files, "", 0, err)
		return nil, err
	}
	if !validAttachmentLimits(s.limits) {
		err := newFileError("", "invalid_attachment_limits", false, nil)
		s.logFailure("policy", files, "", 0, err)
		return nil, err
	}
	if !s.blobs.Enabled() {
		err := newFileError("", "attachment_store_disabled", false, attachment.ErrStoreDisabled)
		s.logFailure("policy", files, "", 0, err)
		return nil, err
	}
	if len(files) > s.limits.MaxFilesPerMessage {
		err := newFileError("", "too_many_files", false, nil)
		s.logFailure("policy", files, "", 0, err)
		return nil, err
	}
	if err := preflightDeclaredSizes(files, s.limits); err != nil {
		s.logFailure("preflight", files, fileFailureID(err), 0, err)
		return nil, err
	}

	resolved := make([]FileRef, 0, len(files))
	for _, file := range files {
		metadata, err := s.client.ResolveFile(ctx, file)
		if err != nil {
			s.logFailure("metadata", files, file.ID, 0, err)
			return nil, err
		}
		resolved = append(resolved, metadata)
	}
	if err := preflightDeclaredSizes(resolved, s.limits); err != nil {
		s.logFailure("metadata", resolved, fileFailureID(err), 0, err)
		return nil, err
	}

	out := make([]attachment.Descriptor, 0, len(resolved))
	var totalBytes int64
	for _, file := range resolved {
		remaining := s.limits.MaxTotalBytes - totalBytes
		maxBytes := min(s.limits.MaxFileBytes, remaining)
		if maxBytes <= 0 {
			err := newFileError(file.ID, totalSizeExceededReason, false, attachment.ErrTooLarge)
			s.logFailure("policy", resolved, file.ID, totalBytes, err)
			return nil, err
		}
		body, err := s.client.DownloadFile(ctx, file)
		if err != nil {
			s.logFailure("download", resolved, file.ID, totalBytes, err)
			return nil, err
		}
		if body == nil {
			err = newFileError(file.ID, "download_unavailable", true, nil)
			s.logFailure("download", resolved, file.ID, totalBytes, err)
			return nil, err
		}
		persisted, persistErr := s.blobs.Persist(ctx, file.descriptor(), body, maxBytes)
		_ = body.Close()
		if persistErr != nil {
			if errors.Is(persistErr, attachment.ErrTooLarge) {
				reason := fileSizeExceededReason
				if remaining < s.limits.MaxFileBytes {
					reason = totalSizeExceededReason
				}
				err = newFileError(file.ID, reason, false, persistErr)
			} else {
				err = newFileError(file.ID, "storage_unavailable", true, persistErr)
			}
			s.logFailure("persistence", resolved, file.ID, totalBytes, err)
			return nil, err
		}
		if persisted.Blob == nil || strings.TrimSpace(persisted.Blob.Path) == "" || persisted.SizeBytes < 0 {
			err = newFileError(file.ID, "invalid_persisted_blob", true, nil)
			s.logFailure("persistence", resolved, file.ID, totalBytes, err)
			return nil, err
		}
		if persisted.SizeBytes > remaining {
			err = newFileError(file.ID, totalSizeExceededReason, false, attachment.ErrTooLarge)
			s.logFailure("persistence", resolved, file.ID, totalBytes, err)
			return nil, err
		}
		totalBytes += persisted.SizeBytes
		out = append(out, persisted)
	}

	s.logger.Info().
		Str("stage", "persistence").
		Str("reason", "accepted").
		Str("mime_class", fileSetMIMEClass(resolved, "")).
		Str("settlement_class", "accepted").
		Strs("file_ids", safeFileIDs(resolved)).
		Int("file_count", len(out)).
		Int64("declared_bytes", declaredFileBytes(resolved)).
		Int64("total_bytes", totalBytes).
		Msg("persisted inbound Slack files")
	return attachment.NormalizeList(out), nil
}

func preflightDeclaredSizes(files []FileRef, limits attachment.Limits) error {
	var total int64
	for _, file := range files {
		size := file.SizeBytes
		if size < 0 {
			size = 0
		}
		if size > limits.MaxFileBytes {
			return newFileError(file.ID, fileSizeExceededReason, false, attachment.ErrTooLarge)
		}
		if size > limits.MaxTotalBytes-total {
			return newFileError(file.ID, totalSizeExceededReason, false, attachment.ErrTooLarge)
		}
		total += size
	}
	return nil
}

func validAttachmentLimits(limits attachment.Limits) bool {
	return limits.MaxFilesPerMessage > 0 && limits.MaxFileBytes > 0 && limits.MaxTotalBytes >= limits.MaxFileBytes
}

func fileFailureReason(err error) string {
	var fileErr *fileError
	if errors.As(err, &fileErr) {
		return sanitizeFailureCode(fileErr.code)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if code := sanitizeFailureCode(apiErr.Code); code != mediaUnavailableReason {
			return code
		}
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return "file_access_denied"
		case http.StatusTooManyRequests:
			return "slack_rate_limited"
		default:
			return "slack_file_unavailable"
		}
	}
	return mediaUnavailableReason
}

func sanitizeFailureCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > 64 {
		return mediaUnavailableReason
	}
	for _, r := range code {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return mediaUnavailableReason
	}
	return code
}

func fileFailureID(err error) string {
	var fileErr *fileError
	if errors.As(err, &fileErr) {
		return strings.TrimSpace(fileErr.fileID)
	}
	return ""
}

func fileSetMIMEClass(files []FileRef, fileID string) string {
	class := ""
	for _, file := range files {
		if fileID != "" && strings.TrimSpace(file.ID) != strings.TrimSpace(fileID) {
			continue
		}
		current := fileMIMEClass(file.MIMEType)
		if class == "" {
			class = current
			continue
		}
		if current != class {
			return "mixed"
		}
	}
	if class == "" {
		return unknownDiagnosticValue
	}
	return class
}

func fileMIMEClass(mimeType string) string {
	prefix, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(mimeType)), "/")
	if !ok {
		return unknownDiagnosticValue
	}
	switch prefix {
	case "application", "audio", "image", "text", "video":
		return prefix
	default:
		return "other"
	}
}

func declaredFileBytes(files []FileRef) int64 {
	var total int64
	for _, file := range files {
		if file.SizeBytes <= 0 {
			continue
		}
		if file.SizeBytes > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += file.SizeBytes
	}
	return total
}

func fileSettlementClass(err error) string {
	if IsRetryableSlackError(err) {
		return "retry"
	}
	return "terminal"
}

func safeFileIDs(files []FileRef) []string {
	ids := make([]string, 0, len(files))
	for _, file := range files {
		ids = append(ids, safeFileID(file.ID))
	}
	return ids
}

func (s *currentFileIngestor) logFailure(stage string, files []FileRef, fileID string, totalBytes int64, err error) {
	s.logger.Warn().
		Err(err).
		Str("stage", stage).
		Str("reason", fileFailureReason(err)).
		Str("file_id", safeFileID(fileID)).
		Str("mime_class", fileSetMIMEClass(files, fileID)).
		Str("settlement_class", fileSettlementClass(err)).
		Int("file_count", len(files)).
		Int64("declared_bytes", declaredFileBytes(files)).
		Int64("persisted_bytes", totalBytes).
		Msg("failed to ingest inbound Slack files")
}

func fileIngestError(err error) error {
	if IsRetryableSlackError(err) {
		return fmt.Errorf("ingest Slack files: %w", err)
	}
	return err
}
