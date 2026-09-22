package slackagent

import (
	"context"
	"errors"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

const (
	maxOutboundFileNameBytes = 255
	defaultOutboundMIMEType  = "application/octet-stream"
)

type outboundMediaError struct {
	code string
}

func (e *outboundMediaError) Error() string {
	return "slack media delivery: " + e.code
}

func permanentOutboundMediaError(code string) error {
	return deliverycmd.PermanentError(&outboundMediaError{code: sanitizeFailureCode(code)})
}

func (a *Adapter) deliverMedia(ctx context.Context, locator deliverycmd.Locator, operation deliverycmd.Operation) (deliverycmd.Result, error) {
	request, file, err := a.prepareMediaUpload(locator, operation.Media)
	if err != nil {
		a.logMediaDelivery(operation.Kind, request, "validation", mediaDeliveryReason(err), errorSettlement(err), "")
		return deliverycmd.Result{}, err
	}
	defer func() { _ = file.Close() }()

	fileID, err := a.uploads.UploadFile(ctx, request)
	if err != nil {
		stage, reason, settlement := mediaUploadDiagnostic(err)
		a.logMediaDelivery(operation.Kind, request, stage, reason, settlement, "")
		return deliverycmd.Result{}, err
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" || safeFileID(fileID) != fileID {
		err := deliverycmd.AmbiguousError(&outboundMediaError{code: "invalid_file_id"})
		a.logMediaDelivery(operation.Kind, request, string(uploadStageCompletion), "invalid_file_id", string(deliverycmd.ErrorKindAmbiguous), "")
		return deliverycmd.Result{}, err
	}
	a.logMediaDelivery(operation.Kind, request, string(uploadStageCompletion), "accepted", "success", fileID)
	return deliverycmd.Result{ProviderMessageID: fileID}, nil
}

func (a *Adapter) prepareMediaUpload(locator deliverycmd.Locator, media *deliverycmd.Media) (UploadFileRequest, *os.File, error) {
	if a == nil || a.uploads == nil {
		return UploadFileRequest{}, nil, permanentOutboundMediaError("upload_client_unavailable")
	}
	if media == nil {
		return UploadFileRequest{}, nil, permanentOutboundMediaError("missing_media")
	}
	address, ok, err := DecodeLocator(locator)
	if err != nil || !ok || strings.TrimSpace(address.ThreadID) == "" {
		return UploadFileRequest{}, nil, permanentOutboundMediaError("invalid_locator")
	}
	if a.attachmentLimits.MaxFileBytes <= 0 {
		return UploadFileRequest{}, nil, permanentOutboundMediaError("invalid_attachment_limits")
	}

	localPath := strings.TrimSpace(media.LocalPath)
	if localPath == "" || hasURLScheme(localPath) {
		return UploadFileRequest{}, nil, permanentOutboundMediaError("invalid_local_source")
	}
	pathInfo, err := os.Lstat(localPath)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return UploadFileRequest{}, nil, permanentOutboundMediaError("invalid_local_source")
	}
	if pathInfo.Size() <= 0 {
		return UploadFileRequest{}, nil, permanentOutboundMediaError("empty_file")
	}
	if pathInfo.Size() > a.attachmentLimits.MaxFileBytes {
		return UploadFileRequest{}, nil, permanentOutboundMediaError(fileSizeExceededReason)
	}

	file, err := os.Open(localPath)
	if err != nil {
		return UploadFileRequest{}, nil, permanentOutboundMediaError("file_open_failed")
	}
	fileInfo, err := file.Stat()
	if err != nil || !sameRegularFile(pathInfo, fileInfo) {
		_ = file.Close()
		return UploadFileRequest{}, nil, permanentOutboundMediaError("source_changed")
	}

	request := UploadFileRequest{
		ChannelID: address.ConversationID,
		ThreadTS:  address.ThreadID,
		FileName:  outboundFileName(media.Name, localPath),
		MIMEType:  outboundMIMEType(media.MIMEType, media.Name, localPath),
		Caption:   strings.TrimSpace(media.Caption),
		SizeBytes: fileInfo.Size(),
		Body:      file,
	}
	request.ValidateSource = func() error {
		current, err := file.Stat()
		if err != nil || !sameRegularFile(fileInfo, current) {
			return errors.New("outbound media source changed")
		}
		return nil
	}
	return request, file, nil
}

func hasURLScheme(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != ""
}

func sameRegularFile(want, got os.FileInfo) bool {
	return want != nil && got != nil && want.Mode().IsRegular() && got.Mode().IsRegular() && want.Size() == got.Size() && want.ModTime().Equal(got.ModTime()) && os.SameFile(want, got)
}

func outboundFileName(name, localPath string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(localPath)
	}
	name = filepath.Base(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "attachment"
	}
	return truncateUTF8(name, maxOutboundFileNameBytes)
}

func outboundMIMEType(value, name, localPath string) string {
	if mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value)); err == nil && strings.Contains(mediaType, "/") {
		return mediaType
	}
	fileName := outboundFileName(name, localPath)
	if detected := mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName))); detected != "" {
		if mediaType, _, err := mime.ParseMediaType(detected); err == nil {
			return mediaType
		}
	}
	return defaultOutboundMIMEType
}

func mediaDeliveryReason(err error) string {
	var mediaErr *outboundMediaError
	if errors.As(err, &mediaErr) {
		return mediaErr.code
	}
	return mediaUnavailableReason
}

func errorSettlement(err error) string {
	kind, ok := deliverycmd.ClassifyError(err)
	if !ok {
		return unknownDiagnosticValue
	}
	return string(kind)
}

func (a *Adapter) logMediaDelivery(kind deliverycmd.OperationKind, request UploadFileRequest, stage, reason, settlement, fileID string) {
	event := a.logger.Info()
	if settlement != "success" {
		event = a.logger.Warn()
	}
	event.
		Str("stage", sanitizeFailureCode(stage)).
		Str("reason", sanitizeFailureCode(reason)).
		Str("operation_kind", string(kind)).
		Str("mime_class", fileMIMEClass(request.MIMEType)).
		Str("settlement_class", sanitizeFailureCode(settlement)).
		Str("file_id", safeFileID(fileID)).
		Int64("size_bytes", max(request.SizeBytes, 0)).
		Msg("handled outbound Slack media")
}
