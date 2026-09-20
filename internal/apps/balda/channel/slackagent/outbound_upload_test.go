package slackagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

const (
	testGetUploadURLExternalPath = "/files.getUploadURLExternal"
	testCompleteUploadPath       = "/files.completeUploadExternal"
	testUploadPath               = "/upload"
)

func TestClientUploadsFileThroughExternalSequence(t *testing.T) {
	t.Parallel()
	const (
		token   = "xoxb-upload-secret"
		fileID  = "F123UPLOAD"
		content = "bounded file bytes"
	)
	type observedRequest struct {
		path          string
		authorization string
		contentType   string
		contentLength int64
		body          string
	}
	var requests []observedRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll(%s) error = %v", r.URL.Path, err)
		}
		requests = append(requests, observedRequest{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			contentType:   r.Header.Get("Content-Type"),
			contentLength: r.ContentLength,
			body:          string(body),
		})
		switch r.URL.Path {
		case testGetUploadURLExternalPath:
			_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+serverURL(r)+`/upload/v1/ticket","file_id":"`+fileID+`"}`)
		case "/upload/v1/ticket":
			_, _ = io.WriteString(w, "OK")
		case testCompleteUploadPath:
			_, _ = io.WriteString(w, `{"ok":true,"files":[{"id":"`+fileID+`","title":"report.txt"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client := NewClientWithBaseURL(server.URL, token)
	client.http = server.Client()
	client.validateFileURL = exactTestServerValidator(t, server.URL)
	got, err := client.UploadFile(context.Background(), UploadFileRequest{
		ChannelID: "C123",
		ThreadTS:  "1712345678.000100",
		FileName:  "report.txt",
		MIMEType:  "text/plain",
		Caption:   "result",
		SizeBytes: int64(len(content)),
		Body:      strings.NewReader(content),
	})
	if err != nil {
		t.Fatalf("UploadFile() error = %v", err)
	}
	if got != fileID {
		t.Fatalf("UploadFile() = %q, want %q", got, fileID)
	}
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	if requests[0].path != testGetUploadURLExternalPath || requests[0].authorization != "Bearer "+token {
		t.Fatalf("ticket request = %+v", requests[0])
	}
	if requests[1] != (observedRequest{
		path:          "/upload/v1/ticket",
		contentType:   "text/plain",
		contentLength: int64(len(content)),
		body:          content,
	}) {
		t.Fatalf("byte request = %+v", requests[1])
	}
	if requests[2].path != testCompleteUploadPath || requests[2].authorization != "Bearer "+token {
		t.Fatalf("completion request = %+v", requests[2])
	}

	var ticket uploadTicketRequest
	if err := json.Unmarshal([]byte(requests[0].body), &ticket); err != nil {
		t.Fatalf("decode ticket request: %v", err)
	}
	if ticket.FileName != "report.txt" || ticket.Length != int64(len(content)) {
		t.Fatalf("ticket request = %+v", ticket)
	}
	var completion completeUploadRequest
	if err := json.Unmarshal([]byte(requests[2].body), &completion); err != nil {
		t.Fatalf("decode completion request: %v", err)
	}
	wantCompletion := completeUploadRequest{
		Files:          []completeUploadFile{{ID: fileID, Title: "report.txt"}},
		ChannelID:      "C123",
		ThreadTS:       "1712345678.000100",
		InitialComment: "result",
	}
	if !reflect.DeepEqual(completion, wantCompletion) {
		t.Fatalf("completion request = %+v, want %+v", completion, wantCompletion)
	}
}

func TestClientRejectsUnsafeUploadTicketBeforeBytes(t *testing.T) {
	t.Parallel()
	const privateURL = "http://files.slack.com/upload/v1/private-ticket"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+privateURL+`","file_id":"F123"}`)
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-secret")

	_, err := client.UploadFile(context.Background(), UploadFileRequest{
		ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", SizeBytes: 1, Body: strings.NewReader("x"),
	})
	if err == nil {
		t.Fatal("UploadFile() error = nil, want unsafe ticket error")
	}
	if calls != 1 {
		t.Fatalf("Slack API calls = %d, want ticket only", calls)
	}
	if strings.Contains(err.Error(), privateURL) || strings.Contains(err.Error(), "private-ticket") || strings.Contains(err.Error(), "xoxb-secret") {
		t.Fatalf("UploadFile() error leaked private material: %v", err)
	}
}

func TestClientRejectsMalformedUploadTicketIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"upload_url":"https://files.slack.com/upload/v1/ticket","file_id":""}`)
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-secret")

	_, err := client.UploadFile(context.Background(), UploadFileRequest{
		ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", SizeBytes: 1, Body: strings.NewReader("x"),
	})
	var uploadErr *mediaUploadError
	if !errors.As(err, &uploadErr) || uploadErr.stage != uploadStageTicket || uploadErr.code != malformedResponseCode {
		t.Fatalf("UploadFile() error = %T %v", err, err)
	}
}

func TestClientRejectsUploadRedirectWithoutFollowing(t *testing.T) {
	t.Parallel()
	var targetCalls int
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls++
	}))
	t.Cleanup(target.Close)
	var source *httptest.Server
	source = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testGetUploadURLExternalPath:
			_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+source.URL+testUploadPath+`","file_id":"F123"}`)
		case testUploadPath:
			http.Redirect(w, r, target.URL+"/stolen", http.StatusFound)
		default:
			t.Fatalf("unexpected path = %q", r.URL.Path)
		}
	}))
	t.Cleanup(source.Close)
	client := NewClientWithBaseURL(source.URL, "xoxb-secret")
	client.http = source.Client()
	client.validateFileURL = exactTestServerValidator(t, source.URL)

	_, err := client.UploadFile(context.Background(), UploadFileRequest{
		ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", SizeBytes: 1, Body: strings.NewReader("x"),
	})
	if err == nil {
		t.Fatal("UploadFile() error = nil, want redirect rejection")
	}
	if kind, ok := deliverycmd.ClassifyError(err); !ok || kind != deliverycmd.ErrorKindPermanent {
		t.Fatalf("UploadFile() error kind = %q, want permanent (err: %v)", kind, err)
	}
	if targetCalls != 0 {
		t.Fatalf("redirect target calls = %d, want 0", targetCalls)
	}
	if strings.Contains(err.Error(), target.URL) || strings.Contains(err.Error(), "xoxb-secret") {
		t.Fatalf("UploadFile() error leaked private material: %v", err)
	}
}

func TestClientRejectsMismatchedCompletionIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testGetUploadURLExternalPath:
			_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+serverURL(r)+`/upload","file_id":"F123"}`)
		case testUploadPath:
			_, _ = io.WriteString(w, "OK")
		case testCompleteUploadPath:
			_, _ = io.WriteString(w, `{"ok":true,"files":[{"id":"F999"}]}`)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-secret")
	client.http = server.Client()
	client.validateFileURL = exactTestServerValidator(t, server.URL)

	_, err := client.UploadFile(context.Background(), UploadFileRequest{
		ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", SizeBytes: 1, Body: strings.NewReader("x"),
	})
	if kind, ok := deliverycmd.ClassifyError(err); !ok || kind != deliverycmd.ErrorKindAmbiguous {
		t.Fatalf("UploadFile() error kind = %q, %v", kind, err)
	}
}

func TestClientRejectsChangedSourceBeforeCompletion(t *testing.T) {
	t.Parallel()
	var completionCalls int
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testGetUploadURLExternalPath:
			_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+server.URL+testUploadPath+`","file_id":"F123"}`)
		case testUploadPath:
			_, _ = io.WriteString(w, "OK")
		case testCompleteUploadPath:
			completionCalls++
			_, _ = io.WriteString(w, `{"ok":true,"files":[{"id":"F123"}]}`)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-secret")
	client.http = server.Client()
	client.validateFileURL = exactTestServerValidator(t, server.URL)

	_, err := client.UploadFile(context.Background(), UploadFileRequest{
		ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", SizeBytes: 1, Body: strings.NewReader("x"),
		ValidateSource: func() error { return errors.New("private path changed") },
	})
	if kind, ok := deliverycmd.ClassifyError(err); !ok || kind != deliverycmd.ErrorKindPermanent {
		t.Fatalf("UploadFile() error kind = %q, want permanent (err: %v)", kind, err)
	}
	if completionCalls != 0 {
		t.Fatalf("completion calls = %d, want 0", completionCalls)
	}
	if strings.Contains(err.Error(), "private path") {
		t.Fatalf("UploadFile() error leaked source detail: %v", err)
	}
}

func TestClientEnforcesExactUploadByteCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		content        string
		size           int64
		wantUploaded   string
		wantCompletion bool
		wantError      bool
	}{
		{name: "short source", content: "x", size: 2, wantError: true},
		{name: "extra source", content: "xy", size: 1, wantUploaded: "x", wantCompletion: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var uploaded string
			var completionCalls int
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case testGetUploadURLExternalPath:
					_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+server.URL+testUploadPath+`","file_id":"F123"}`)
				case testUploadPath:
					body, _ := io.ReadAll(r.Body)
					uploaded = string(body)
					_, _ = io.WriteString(w, "OK")
				case testCompleteUploadPath:
					completionCalls++
					_, _ = io.WriteString(w, `{"ok":true,"files":[{"id":"F123"}]}`)
				}
			}))
			t.Cleanup(server.Close)
			client := NewClientWithBaseURL(server.URL, "xoxb-secret")
			client.http = server.Client()
			client.validateFileURL = exactTestServerValidator(t, server.URL)

			_, err := client.UploadFile(t.Context(), UploadFileRequest{
				ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", SizeBytes: test.size, Body: strings.NewReader(test.content),
			})
			if got := err != nil; got != test.wantError {
				t.Fatalf("UploadFile() error = %v, want error %v", err, test.wantError)
			}
			if test.wantUploaded != "" && uploaded != test.wantUploaded {
				t.Fatalf("uploaded bytes = %q, want %q", uploaded, test.wantUploaded)
			}
			if got := completionCalls > 0; got != test.wantCompletion {
				t.Fatalf("completion called = %v, want %v", got, test.wantCompletion)
			}
		})
	}
}

func TestClientClassifiesUploadFailuresByStage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		ticketStatus     int
		ticketBody       string
		byteStatus       int
		completionStatus int
		completionBody   string
		wantKind         deliverycmd.ErrorKind
		wantStage        string
		wantCompletion   bool
	}{
		{name: "ticket server failure", ticketStatus: http.StatusBadGateway, wantKind: deliverycmd.ErrorKindRetryable, wantStage: string(uploadStageTicket)},
		{name: "ticket missing scope", ticketBody: `{"ok":false,"error":"missing_scope"}`, wantKind: deliverycmd.ErrorKindPermanent, wantStage: string(uploadStageTicket)},
		{name: "ticket malformed", ticketBody: `{`, wantKind: deliverycmd.ErrorKindRetryable, wantStage: string(uploadStageTicket)},
		{name: "byte server failure", byteStatus: http.StatusBadGateway, wantKind: deliverycmd.ErrorKindRetryable, wantStage: string(uploadStageBytes)},
		{name: "byte rejection", byteStatus: http.StatusForbidden, wantKind: deliverycmd.ErrorKindPermanent, wantStage: string(uploadStageBytes)},
		{name: "completion rate limit", completionStatus: http.StatusTooManyRequests, wantKind: deliverycmd.ErrorKindRetryable, wantStage: string(uploadStageCompletion), wantCompletion: true},
		{name: "completion server uncertainty", completionStatus: http.StatusBadGateway, wantKind: deliverycmd.ErrorKindAmbiguous, wantStage: string(uploadStageCompletion), wantCompletion: true},
		{name: "completion malformed", completionBody: `{`, wantKind: deliverycmd.ErrorKindAmbiguous, wantStage: string(uploadStageCompletion), wantCompletion: true},
		{name: "completion definitive transient", completionBody: `{"ok":false,"error":"ratelimited"}`, wantKind: deliverycmd.ErrorKindRetryable, wantStage: string(uploadStageCompletion), wantCompletion: true},
		{name: "completion permanent", completionBody: `{"ok":false,"error":"missing_scope"}`, wantKind: deliverycmd.ErrorKindPermanent, wantStage: string(uploadStageCompletion), wantCompletion: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var server *httptest.Server
			var completionCalls int
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case testGetUploadURLExternalPath:
					if test.ticketStatus != 0 {
						w.WriteHeader(test.ticketStatus)
						return
					}
					if test.ticketBody != "" {
						_, _ = io.WriteString(w, test.ticketBody)
						return
					}
					_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+server.URL+testUploadPath+`","file_id":"F123"}`)
				case testUploadPath:
					status := test.byteStatus
					if status == 0 {
						status = http.StatusOK
					}
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "upload response")
				case testCompleteUploadPath:
					completionCalls++
					if test.completionStatus != 0 {
						w.WriteHeader(test.completionStatus)
						return
					}
					if test.completionBody != "" {
						_, _ = io.WriteString(w, test.completionBody)
						return
					}
					_, _ = io.WriteString(w, `{"ok":true,"files":[{"id":"F123"}]}`)
				}
			}))
			t.Cleanup(server.Close)
			client := NewClientWithBaseURL(server.URL, "xoxb-secret")
			client.http = server.Client()
			client.validateFileURL = exactTestServerValidator(t, server.URL)

			_, err := client.UploadFile(context.Background(), UploadFileRequest{
				ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", SizeBytes: 1, Body: strings.NewReader("x"),
			})
			kind, ok := deliverycmd.ClassifyError(err)
			if !ok || kind != test.wantKind {
				t.Fatalf("UploadFile() error kind = %q, want %q (err: %v)", kind, test.wantKind, err)
			}
			stage, _, settlement := mediaUploadDiagnostic(err)
			if stage != test.wantStage || settlement != string(test.wantKind) {
				t.Fatalf("diagnostic = %q/%q, want %q/%q", stage, settlement, test.wantStage, test.wantKind)
			}
			if got := completionCalls > 0; got != test.wantCompletion {
				t.Fatalf("completion called = %v, want %v", got, test.wantCompletion)
			}
		})
	}
}

func TestClientClassifiesCompletionTransportFailureAsAmbiguous(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testGetUploadURLExternalPath:
			_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+server.URL+`/upload/private-ticket","file_id":"F123"}`)
		case "/upload/private-ticket":
			_, _ = io.WriteString(w, "OK")
		case testCompleteUploadPath:
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijacking")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("Hijack() error = %v", err)
			}
			_ = conn.Close()
		}
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-secret")
	client.http = server.Client()
	client.validateFileURL = exactTestServerValidator(t, server.URL)

	_, err := client.UploadFile(context.Background(), UploadFileRequest{
		ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", Caption: "private-caption", SizeBytes: 1, Body: strings.NewReader("x"),
	})
	kind, ok := deliverycmd.ClassifyError(err)
	if !ok || kind != deliverycmd.ErrorKindAmbiguous {
		t.Fatalf("UploadFile() error kind = %q, want ambiguous (err: %v)", kind, err)
	}
	for _, private := range []string{"xoxb-secret", server.URL, "private-ticket", "private-caption"} {
		if strings.Contains(err.Error(), private) {
			t.Fatalf("UploadFile() error leaked %q: %v", private, err)
		}
	}
}

func TestClientClassifiesCompletionCancellationAsAmbiguous(t *testing.T) {
	t.Parallel()
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testGetUploadURLExternalPath:
			_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+server.URL+testUploadPath+`","file_id":"F123"}`)
		case testUploadPath:
			_, _ = io.WriteString(w, "OK")
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	baseTransport := server.Client().Transport
	client := NewClientWithBaseURL(server.URL, "xoxb-secret")
	client.http = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == testCompleteUploadPath {
			cancel()
			return nil, context.Canceled
		}
		return baseTransport.RoundTrip(req)
	})}
	client.validateFileURL = exactTestServerValidator(t, server.URL)

	_, err := client.UploadFile(ctx, UploadFileRequest{
		ChannelID: "C123", ThreadTS: "1.2", FileName: "report.txt", MIMEType: "text/plain", SizeBytes: 1, Body: strings.NewReader("x"),
	})
	kind, ok := deliverycmd.ClassifyError(err)
	if !ok || kind != deliverycmd.ErrorKindAmbiguous {
		t.Fatalf("UploadFile() error kind = %q, want ambiguous (err: %v)", kind, err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func serverURL(r *http.Request) string {
	return "https://" + r.Host
}
