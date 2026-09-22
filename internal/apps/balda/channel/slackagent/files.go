package slackagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
)

const slackFileHost = "files.slack.com"

type rawFile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Title              string `json:"title"`
	MIMEType           string `json:"mimetype"`
	SizeBytes          int64  `json:"size"`
	Mode               string `json:"mode"`
	FileAccess         string `json:"file_access"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
}

// FileRef is safe Slack-local metadata for one inbound file. Private URLs stay unexported.
type FileRef struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Title      string `json:"title,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Mode       string `json:"mode,omitempty"`
	FileAccess string `json:"file_access,omitempty"`

	privateURL         string
	privateDownloadURL string
}

// FileClient resolves Slack file metadata and opens authenticated private content.
type FileClient interface {
	ResolveFile(ctx context.Context, file FileRef) (FileRef, error)
	DownloadFile(ctx context.Context, file FileRef) (io.ReadCloser, error)
}

type fileInfoResponse struct {
	OK    bool    `json:"ok"`
	Error string  `json:"error"`
	File  rawFile `json:"file"`
}

type fileError struct {
	fileID    string
	code      string
	retryable bool
	cause     error
}

func (e *fileError) Error() string {
	if e.fileID == "" {
		return "slack file " + e.code
	}
	return fmt.Sprintf("slack file %s: %s", e.fileID, e.code)
}

func (e *fileError) Unwrap() error { return e.cause }

func normalizeRawFiles(files []rawFile) []FileRef {
	if len(files) == 0 {
		return nil
	}
	out := make([]FileRef, 0, len(files))
	for _, file := range files {
		out = append(out, normalizeRawFile(file))
	}
	return out
}

func normalizeRawFile(file rawFile) FileRef {
	size := file.SizeBytes
	if size < 0 {
		size = 0
	}
	return FileRef{
		ID:                 strings.TrimSpace(file.ID),
		Name:               strings.TrimSpace(file.Name),
		Title:              strings.TrimSpace(file.Title),
		MIMEType:           strings.TrimSpace(file.MIMEType),
		SizeBytes:          size,
		Mode:               strings.TrimSpace(file.Mode),
		FileAccess:         strings.TrimSpace(file.FileAccess),
		privateURL:         strings.TrimSpace(file.URLPrivate),
		privateDownloadURL: strings.TrimSpace(file.URLPrivateDownload),
	}
}

func normalizeFileRef(file FileRef) FileRef {
	return normalizeRawFile(rawFile{
		ID:                 file.ID,
		Name:               file.Name,
		Title:              file.Title,
		MIMEType:           file.MIMEType,
		SizeBytes:          file.SizeBytes,
		Mode:               file.Mode,
		FileAccess:         file.FileAccess,
		URLPrivate:         file.privateURL,
		URLPrivateDownload: file.privateDownloadURL,
	})
}

// ResolveFile resolves incomplete and Slack Connect placeholder metadata.
func (c *Client) ResolveFile(ctx context.Context, file FileRef) (FileRef, error) {
	file = normalizeFileRef(file)
	if file.ID == "" {
		return FileRef{}, newFileError("", "missing_file_id", false, nil)
	}
	if file.FileAccess != checkFileInfoAccess && file.downloadURL() != "" {
		return file, nil
	}

	var response fileInfoResponse
	if err := c.getJSON(ctx, "files.info", url.Values{"file": {file.ID}}, &response); err != nil {
		return FileRef{}, err
	}
	if !response.OK {
		code := strings.TrimSpace(response.Error)
		return FileRef{}, &APIError{Method: "files.info", StatusCode: http.StatusOK, Code: code, Message: code, Retryable: retryableSlackCode(code)}
	}
	resolved := normalizeRawFile(response.File)
	if resolved.ID == "" || resolved.ID != file.ID || resolved.downloadURL() == "" {
		return FileRef{}, newFileError(file.ID, "malformed_metadata", false, nil)
	}
	return resolved, nil
}

// DownloadFile opens an authenticated stream for one resolved Slack file.
func (c *Client) DownloadFile(ctx context.Context, file FileRef) (io.ReadCloser, error) {
	file = normalizeFileRef(file)
	if file.ID == "" {
		return nil, newFileError("", "missing_file_id", false, nil)
	}
	if c == nil || strings.TrimSpace(c.token) == "" {
		return nil, newFileError(file.ID, "client_unavailable", false, nil)
	}
	parsed, err := url.Parse(file.downloadURL())
	if err != nil {
		return nil, newFileError(file.ID, "unsafe_download_url", false, err)
	}
	validator := c.validateFileURL
	if validator == nil {
		validator = validateSlackFileURL
	}
	if err := validator(parsed); err != nil {
		return nil, newFileError(file.ID, "unsafe_download_url", false, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, newFileError(file.ID, "invalid_download_request", false, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	httpClient := c.fileHTTPClient(file.ID, validator)
	resp, err := httpClient.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		var redirectErr *fileError
		if errors.As(err, &redirectErr) {
			return nil, redirectErr
		}
		return nil, newFileError(file.ID, "download_failed", true, err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_ = resp.Body.Close()
		return nil, &APIError{
			Method:     "files.download",
			StatusCode: resp.StatusCode,
			Message:    http.StatusText(resp.StatusCode),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			Retryable:  resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError,
		}
	}
	return resp.Body, nil
}

func (f FileRef) downloadURL() string {
	if f.privateDownloadURL != "" {
		return f.privateDownloadURL
	}
	return f.privateURL
}

func (f FileRef) descriptor() attachment.Descriptor {
	kind := attachment.KindDocument
	if strings.HasPrefix(strings.ToLower(f.MIMEType), "image/") {
		kind = attachment.KindPhoto
	}
	name := f.Name
	if name == "" {
		name = f.Title
	}
	return attachment.Descriptor{
		Kind:      kind,
		FileID:    f.ID,
		FileName:  name,
		MIMEType:  f.MIMEType,
		SizeBytes: f.SizeBytes,
	}
}

func (c *Client) fileHTTPClient(fileID string, validator func(*url.URL) error) *http.Client {
	base := c.http
	if base == nil {
		base = &http.Client{Timeout: defaultHTTPClientTimeout}
	}
	clone := *base
	previousCheck := base.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validator(req.URL); err != nil {
			return newFileError(fileID, "unsafe_redirect", false, err)
		}
		if len(via) >= 10 {
			return errors.New("too many Slack file redirects")
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		if previousCheck != nil {
			return previousCheck(req, via)
		}
		return nil
	}
	return &clone
}

func validateSlackFileURL(value *url.URL) error {
	if value == nil || value.Scheme != "https" || !strings.EqualFold(value.Hostname(), slackFileHost) {
		return errors.New("slack file URL must use the approved HTTPS host")
	}
	if port := value.Port(); port != "" && port != strconv.Itoa(443) {
		return errors.New("slack file URL must use the HTTPS port")
	}
	if value.User != nil || value.Fragment != "" || value.Path == "" {
		return errors.New("slack file URL contains unsupported components")
	}
	return nil
}

func newFileError(fileID, code string, retryable bool, cause error) error {
	return &fileError{
		fileID:    safeFileID(fileID),
		code:      strings.TrimSpace(code),
		retryable: retryable,
		cause:     cause,
	}
}

func safeFileID(fileID string) string {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return ""
	}
	if len(fileID) > 64 {
		return unknownDiagnosticValue
	}
	for _, r := range fileID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return unknownDiagnosticValue
	}
	return fileID
}
