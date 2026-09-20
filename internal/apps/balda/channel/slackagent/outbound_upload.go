package slackagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

type uploadStage string

const (
	uploadStageTicket     uploadStage = "ticket"
	uploadStageBytes      uploadStage = "bytes"
	uploadStageCompletion uploadStage = "completion"

	getUploadURLExternalMethod = "files.getUploadURLExternal"
	completeUploadMethod       = "files.completeUploadExternal"
	uploadRequestFailedCode    = "request_failed"
	uploadResponseFailedCode   = "response_failed"
)

var errUploadRedirect = errors.New("slack media upload redirect rejected")

// MediaUploadClient uploads one local media stream into a Slack conversation.
type MediaUploadClient interface {
	UploadFile(ctx context.Context, input UploadFileRequest) (string, error)
}

// UploadFileRequest contains one validated media stream and its Slack target.
type UploadFileRequest struct {
	ChannelID      string
	ThreadTS       string
	FileName       string
	MIMEType       string
	Caption        string
	SizeBytes      int64
	Body           io.Reader
	ValidateSource func() error
}

type uploadTicketRequest struct {
	FileName string `json:"filename"`
	Length   int64  `json:"length"`
}

type uploadTicketResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
	UploadURL string `json:"upload_url"`
	FileID    string `json:"file_id"`
}

type completeUploadFile struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

type completeUploadRequest struct {
	Files          []completeUploadFile `json:"files"`
	ChannelID      string               `json:"channel_id"`
	ThreadTS       string               `json:"thread_ts"`
	InitialComment string               `json:"initial_comment,omitempty"`
}

type completeUploadResponse struct {
	OK    bool                 `json:"ok"`
	Error string               `json:"error"`
	Files []completeUploadFile `json:"files"`
}

type mediaUploadError struct {
	stage uploadStage
	code  string
	cause error
}

func (e *mediaUploadError) Error() string {
	return fmt.Sprintf("slack media upload %s: %s", e.stage, e.code)
}

func (e *mediaUploadError) Unwrap() error { return e.cause }

func newMediaUploadError(stage uploadStage, code string, cause error) error {
	return &mediaUploadError{stage: stage, code: strings.TrimSpace(code), cause: cause}
}

// UploadFile executes Slack's external upload sequence for one bounded stream.
func (c *Client) UploadFile(ctx context.Context, input UploadFileRequest) (string, error) {
	input = normalizeUploadFileRequest(input)
	if err := validateUploadFileRequest(input); err != nil {
		return "", classifyMediaUploadError(err)
	}
	ticket, err := c.requestUploadTicket(ctx, input.FileName, input.SizeBytes)
	if err != nil {
		return "", classifyMediaUploadError(err)
	}
	if err := c.uploadFileBytes(ctx, ticket.UploadURL, input); err != nil {
		return "", classifyMediaUploadError(err)
	}
	if input.ValidateSource != nil {
		if err := input.ValidateSource(); err != nil {
			return "", classifyMediaUploadError(newMediaUploadError(uploadStageBytes, "source_changed", err))
		}
	}
	if err := c.completeFileUpload(ctx, ticket.FileID, input); err != nil {
		return "", classifyMediaUploadError(err)
	}
	return ticket.FileID, nil
}

func classifyMediaUploadError(err error) error {
	var uploadErr *mediaUploadError
	if !errors.As(err, &uploadErr) {
		return deliverycmd.RetryableError(newMediaUploadError(uploadStageTicket, "unexpected_failure", nil))
	}
	safeErr := newMediaUploadError(uploadErr.stage, uploadErr.code, nil)
	switch mediaUploadErrorKind(uploadErr) {
	case deliverycmd.ErrorKindPermanent:
		return deliverycmd.PermanentError(safeErr)
	case deliverycmd.ErrorKindAmbiguous:
		return deliverycmd.AmbiguousError(safeErr)
	default:
		return deliverycmd.RetryableError(safeErr)
	}
}

func mediaUploadErrorKind(err *mediaUploadError) deliverycmd.ErrorKind {
	switch err.stage {
	case uploadStageCompletion:
		return completionErrorKind(err)
	case uploadStageBytes:
		if err.code == uploadRequestFailedCode || err.code == uploadResponseFailedCode {
			return deliverycmd.ErrorKindRetryable
		}
		return retryableAPIErrorKind(err.cause)
	case uploadStageTicket:
		if err.code == uploadRequestFailedCode {
			return deliverycmd.ErrorKindRetryable
		}
		return retryableAPIErrorKind(err.cause)
	default:
		return deliverycmd.ErrorKindPermanent
	}
}

func completionErrorKind(err *mediaUploadError) deliverycmd.ErrorKind {
	if err.code == malformedResponseCode {
		return deliverycmd.ErrorKindAmbiguous
	}
	var apiErr *APIError
	if !errors.As(err.cause, &apiErr) {
		if err.code == uploadRequestFailedCode || err.code == uploadResponseFailedCode {
			return deliverycmd.ErrorKindAmbiguous
		}
		return deliverycmd.ErrorKindPermanent
	}
	if apiErr.StatusCode == http.StatusTooManyRequests {
		return deliverycmd.ErrorKindRetryable
	}
	if apiErr.StatusCode >= http.StatusInternalServerError || apiErr.Code == malformedResponseCode {
		return deliverycmd.ErrorKindAmbiguous
	}
	if apiErr.Retryable {
		return deliverycmd.ErrorKindRetryable
	}
	return deliverycmd.ErrorKindPermanent
}

func retryableAPIErrorKind(err error) deliverycmd.ErrorKind {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Retryable {
		return deliverycmd.ErrorKindRetryable
	}
	return deliverycmd.ErrorKindPermanent
}

func mediaUploadDiagnostic(err error) (stage, reason, settlement string) {
	var uploadErr *mediaUploadError
	if errors.As(err, &uploadErr) {
		stage = string(uploadErr.stage)
		reason = uploadErr.code
	}
	if kind, ok := deliverycmd.ClassifyError(err); ok {
		settlement = string(kind)
	}
	return stage, reason, settlement
}

func normalizeUploadFileRequest(input UploadFileRequest) UploadFileRequest {
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.ThreadTS = strings.TrimSpace(input.ThreadTS)
	input.FileName = strings.TrimSpace(input.FileName)
	input.MIMEType = strings.TrimSpace(input.MIMEType)
	input.Caption = strings.TrimSpace(input.Caption)
	return input
}

func validateUploadFileRequest(input UploadFileRequest) error {
	switch {
	case input.ChannelID == "":
		return newMediaUploadError(uploadStageTicket, "missing_channel", nil)
	case input.ThreadTS == "":
		return newMediaUploadError(uploadStageTicket, "missing_thread", nil)
	case input.FileName == "":
		return newMediaUploadError(uploadStageTicket, "missing_filename", nil)
	case input.MIMEType == "":
		return newMediaUploadError(uploadStageTicket, "missing_mime_type", nil)
	case input.SizeBytes <= 0:
		return newMediaUploadError(uploadStageTicket, "invalid_size", nil)
	case input.Body == nil:
		return newMediaUploadError(uploadStageTicket, "missing_body", nil)
	default:
		return nil
	}
}

func (c *Client) requestUploadTicket(ctx context.Context, fileName string, sizeBytes int64) (uploadTicketResponse, error) {
	var response uploadTicketResponse
	if err := c.postJSON(ctx, getUploadURLExternalMethod, uploadTicketRequest{FileName: fileName, Length: sizeBytes}, &response); err != nil {
		return uploadTicketResponse{}, newMediaUploadError(uploadStageTicket, uploadRequestFailedCode, err)
	}
	if !response.OK {
		code := strings.TrimSpace(response.Error)
		apiErr := &APIError{Method: getUploadURLExternalMethod, StatusCode: http.StatusOK, Code: code, Message: code, Retryable: retryableSlackCode(code)}
		return uploadTicketResponse{}, newMediaUploadError(uploadStageTicket, "rejected", apiErr)
	}
	response.UploadURL = strings.TrimSpace(response.UploadURL)
	response.FileID = strings.TrimSpace(response.FileID)
	if response.UploadURL == "" || response.FileID == "" || safeFileID(response.FileID) != response.FileID {
		return uploadTicketResponse{}, newMediaUploadError(uploadStageTicket, malformedResponseCode, nil)
	}
	return response, nil
}

func (c *Client) uploadFileBytes(ctx context.Context, rawURL string, input UploadFileRequest) error {
	uploadURL, err := url.Parse(rawURL)
	if err != nil {
		return newMediaUploadError(uploadStageTicket, "unsafe_upload_url", err)
	}
	validator := c.validateFileURL
	if validator == nil {
		validator = validateSlackFileURL
	}
	if err := validator(uploadURL); err != nil {
		return newMediaUploadError(uploadStageTicket, "unsafe_upload_url", err)
	}

	body := io.NopCloser(io.LimitReader(input.Body, input.SizeBytes))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL.String(), body)
	if err != nil {
		return newMediaUploadError(uploadStageBytes, "invalid_request", err)
	}
	req.ContentLength = input.SizeBytes
	req.Header.Set("Content-Type", input.MIMEType)

	httpClient := c.uploadHTTPClient()
	resp, err := httpClient.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if errors.Is(err, errUploadRedirect) {
			return newMediaUploadError(uploadStageBytes, "unsafe_redirect", err)
		}
		return newMediaUploadError(uploadStageBytes, uploadRequestFailedCode, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := readLimitedResponseBody(resp.Body); err != nil {
		return newMediaUploadError(uploadStageBytes, uploadResponseFailedCode, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		apiErr := &APIError{
			Method:     "files.uploadExternal.bytes",
			StatusCode: resp.StatusCode,
			Message:    http.StatusText(resp.StatusCode),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError,
		}
		return newMediaUploadError(uploadStageBytes, "rejected", apiErr)
	}
	return nil
}

func (c *Client) uploadHTTPClient() *http.Client {
	base := c.http
	if base == nil {
		base = &http.Client{Timeout: defaultHTTPClientTimeout}
	}
	clone := *base
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errUploadRedirect
	}
	return &clone
}

func (c *Client) completeFileUpload(ctx context.Context, fileID string, input UploadFileRequest) error {
	request := completeUploadRequest{
		Files:          []completeUploadFile{{ID: fileID, Title: input.FileName}},
		ChannelID:      input.ChannelID,
		ThreadTS:       input.ThreadTS,
		InitialComment: input.Caption,
	}
	var response completeUploadResponse
	if err := c.postJSON(ctx, completeUploadMethod, request, &response); err != nil {
		return newMediaUploadError(uploadStageCompletion, uploadRequestFailedCode, err)
	}
	if !response.OK {
		code := strings.TrimSpace(response.Error)
		apiErr := &APIError{Method: completeUploadMethod, StatusCode: http.StatusOK, Code: code, Message: code, Retryable: retryableSlackCode(code)}
		return newMediaUploadError(uploadStageCompletion, "rejected", apiErr)
	}
	if len(response.Files) != 1 || strings.TrimSpace(response.Files[0].ID) != fileID {
		return newMediaUploadError(uploadStageCompletion, malformedResponseCode, nil)
	}
	return nil
}
