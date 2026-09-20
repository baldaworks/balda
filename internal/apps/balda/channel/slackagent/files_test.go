package slackagent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestClientResolveFileUsesFilesInfoForPlaceholder(t *testing.T) {
	t.Parallel()
	const fileID = "F123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files.info" || r.URL.Query().Get("file") != fileID {
			t.Errorf("request = %s?%s, want /files.info?file=%s", r.URL.Path, r.URL.RawQuery, fileID)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xoxb-test" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"ok":true,"file":{"id":"F123","name":"image.png","mimetype":"image/png","size":42,"url_private_download":"https://files.slack.com/files-pri/resolved"}}`)
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-test")

	got, err := client.ResolveFile(context.Background(), FileRef{ID: fileID, FileAccess: "check_file_info"})
	if err != nil {
		t.Fatalf("ResolveFile() error = %v", err)
	}
	if got.ID != fileID || got.Name != "image.png" || got.MIMEType != "image/png" || got.SizeBytes != 42 {
		t.Fatalf("ResolveFile() = %+v", got)
	}
	if got.downloadURL() != "https://files.slack.com/files-pri/resolved" {
		t.Fatalf("download URL was not retained transiently")
	}
	descriptor := got.descriptor()
	if descriptor.Kind != "photo" || descriptor.FileID != "F123" || descriptor.FileName != "image.png" {
		t.Fatalf("descriptor = %+v", descriptor)
	}
}

func TestClientResolveFileClassifiesMissingScopePermanently(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"error":"missing_scope"}`)
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-test")

	_, err := client.ResolveFile(context.Background(), FileRef{ID: "F123", FileAccess: "check_file_info"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "missing_scope" {
		t.Fatalf("ResolveFile() error = %T %v, want missing_scope APIError", err, err)
	}
	if IsRetryableSlackError(err) {
		t.Fatalf("missing_scope error was classified retryable: %v", err)
	}
}

func TestFileRefDescriptorClassifiesImagesAndDocuments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		file     FileRef
		kind     string
		fileName string
	}{
		{name: "image", file: FileRef{ID: "F1", Name: "photo.png", MIMEType: "image/png"}, kind: "photo", fileName: "photo.png"},
		{name: "document title fallback", file: FileRef{ID: "F2", Title: "report.pdf", MIMEType: "application/pdf"}, kind: "document", fileName: "report.pdf"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.file.descriptor()
			if string(got.Kind) != test.kind || got.FileName != test.fileName || got.FileID != test.file.ID {
				t.Fatalf("descriptor() = %+v, want kind=%s filename=%s", got, test.kind, test.fileName)
			}
		})
	}
}

func TestClientDownloadFileAuthenticatesApprovedStream(t *testing.T) {
	t.Parallel()
	const token = "xoxb-download-secret"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, "private bytes")
	}))
	t.Cleanup(server.Close)
	client := NewClient(token)
	client.http = server.Client()
	client.validateFileURL = exactTestServerValidator(t, server.URL)

	body, err := client.DownloadFile(context.Background(), FileRef{ID: "F123", privateDownloadURL: server.URL + "/private"})
	if err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	defer func() { _ = body.Close() }()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "private bytes" {
		t.Fatalf("downloaded body = %q, want private bytes", got)
	}
}

func TestClientDownloadFileRejectsUnsafeURLWithoutSecrets(t *testing.T) {
	t.Parallel()
	const (
		token      = "xoxb-never-leak"
		privateURL = "http://files.slack.com/files-pri/private-secret"
	)
	client := NewClient(token)

	_, err := client.DownloadFile(context.Background(), FileRef{ID: "F123", privateDownloadURL: privateURL})
	if err == nil {
		t.Fatal("DownloadFile() error = nil, want unsafe URL failure")
	}
	if IsRetryableSlackError(err) {
		t.Fatalf("unsafe URL error was classified retryable: %v", err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), privateURL) || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("error leaked private material: %v", err)
	}
}

func TestClientDownloadFileRejectsUnapprovedRedirect(t *testing.T) {
	t.Parallel()
	targetCalls := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls++
	}))
	t.Cleanup(target.Close)
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
	}))
	t.Cleanup(source.Close)
	client := NewClient("xoxb-redirect-secret")
	client.http = source.Client()
	client.validateFileURL = exactTestServerValidator(t, source.URL)

	_, err := client.DownloadFile(context.Background(), FileRef{ID: "F123", privateDownloadURL: source.URL + "/private"})
	if err == nil || IsRetryableSlackError(err) {
		t.Fatalf("DownloadFile() error = %v, want permanent redirect rejection", err)
	}
	if targetCalls != 0 {
		t.Fatalf("redirect target calls = %d, want 0", targetCalls)
	}
	if strings.Contains(err.Error(), target.URL) || strings.Contains(err.Error(), "xoxb-redirect-secret") {
		t.Fatalf("redirect error leaked private material: %v", err)
	}
}

func TestClientDownloadFileClassifiesHTTPFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		status        int
		wantRetryable bool
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, wantRetryable: true},
		{name: "server error", status: http.StatusBadGateway, wantRetryable: true},
		{name: "access denied", status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, "private response body")
			}))
			t.Cleanup(server.Close)
			client := NewClient("xoxb-status-secret")
			client.http = server.Client()
			client.validateFileURL = exactTestServerValidator(t, server.URL)

			_, err := client.DownloadFile(context.Background(), FileRef{ID: "F123", privateDownloadURL: server.URL + "/private"})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != test.status || apiErr.Retryable != test.wantRetryable {
				t.Fatalf("DownloadFile() error = %T %+v", err, apiErr)
			}
			if strings.Contains(err.Error(), "private response body") || strings.Contains(err.Error(), "xoxb-status-secret") {
				t.Fatalf("error leaked response or token: %v", err)
			}
		})
	}
}

func exactTestServerValidator(t *testing.T, rawURL string) func(*url.URL) error {
	t.Helper()
	want, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", rawURL, err)
	}
	return func(got *url.URL) error {
		if got == nil || got.Scheme != want.Scheme || got.Host != want.Host {
			return errors.New("unapproved test file URL")
		}
		return nil
	}
}
