package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSocketURLDerivesEndpointFromScheme(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "https becomes wss", baseURL: "https://mm.example.com", want: "wss://mm.example.com/api/v4/websocket"},
		{name: "http becomes ws", baseURL: "http://mattermost:8065", want: "ws://mattermost:8065/api/v4/websocket"},
		{name: "trailing slash is trimmed", baseURL: "https://mm.example.com/", want: "wss://mm.example.com/api/v4/websocket"},
		{name: "base path is preserved", baseURL: "https://mm.example.com/sub", want: "wss://mm.example.com/sub/api/v4/websocket"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(tc.baseURL, "token", "bot-1")
			got, err := client.WebSocketURL()
			if err != nil {
				t.Fatalf("WebSocketURL() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("WebSocketURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWebSocketURLRejectsUnsupportedScheme(t *testing.T) {
	client := NewClient("ftp://mm.example.com", "token", "bot-1")
	if _, err := client.WebSocketURL(); err == nil {
		t.Fatal("WebSocketURL() error = nil, want an error for a non-http scheme")
	}
}

func TestAPIBaseURLIncludesVersionPrefix(t *testing.T) {
	client := NewClient("https://mm.example.com/", "token", "bot-1")
	if got, want := client.APIBaseURL(), "https://mm.example.com/api/v4"; got != want {
		t.Fatalf("APIBaseURL() = %q, want %q", got, want)
	}
}

func TestValidateConfigRequiresServerURLAndToken(t *testing.T) {
	cases := []struct {
		name      string
		serverURL string
		token     string
		wantError bool
	}{
		{name: "complete config", serverURL: "https://mm.example.com", token: "token"},
		{name: "missing server url", serverURL: "", token: "token", wantError: true},
		{name: "missing token", serverURL: "https://mm.example.com", token: "", wantError: true},
		{name: "relative server url", serverURL: "mm.example.com", token: "token", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(tc.serverURL, tc.token)
			if tc.wantError && err == nil {
				t.Fatal("ValidateConfig() error = nil, want an error")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("ValidateConfig() error = %v, want nil", err)
			}
		})
	}
}

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient(server.URL, "bot-token", "bot-1")
}

func TestCreatePostSendsBearerAuthorization(t *testing.T) {
	var gotAuth string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Post{ID: "post-1"})
	}))

	if _, err := client.CreatePost(context.Background(), "channel-1", "", "hello"); err != nil {
		t.Fatalf("CreatePost() error = %v", err)
	}
	if got, want := gotAuth, "Bearer bot-token"; got != want {
		t.Fatalf("Authorization header = %q, want %q", got, want)
	}
}

func TestCreatePostValidatesInput(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("CreatePost() reached the network with invalid input")
	}))

	if _, err := client.CreatePost(context.Background(), "", "", "hello"); err == nil {
		t.Fatal("CreatePost(no channel) error = nil, want an error")
	}
	if _, err := client.CreatePost(context.Background(), "channel-1", "", "   "); err == nil {
		t.Fatal("CreatePost(no message) error = nil, want an error")
	}
}

func TestCreatePostRejectsResponseWithoutID(t *testing.T) {
	// A 2xx with no post id cannot be correlated later, so it must fail loudly
	// instead of returning a zero value that callers treat as delivered.
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))

	_, err := client.CreatePost(context.Background(), "channel-1", "", "hello")
	if err == nil {
		t.Fatal("CreatePost() error = nil, want an error for a response without id")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("CreatePost() error = %v, want an *APIError", err)
	}
	if got, want := apiErr.ID, "MALFORMED_RESPONSE"; got != want {
		t.Fatalf("APIError.ID = %q, want %q", got, want)
	}
}

func TestAPIErrorIsPopulatedFromResponseBody(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"id":"api.context.permissions.app_error","message":"no permission"}`))
	}))

	_, err := client.CreatePost(context.Background(), "channel-1", "", "hello")
	if err == nil {
		t.Fatal("CreatePost() error = nil, want an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("CreatePost() error = %v, want an *APIError", err)
	}
	if got, want := apiErr.StatusCode, http.StatusForbidden; got != want {
		t.Fatalf("APIError.StatusCode = %d, want %d", got, want)
	}
	if got, want := apiErr.ID, "api.context.permissions.app_error"; got != want {
		t.Fatalf("APIError.ID = %q, want %q", got, want)
	}
	if got, want := apiErr.Message, "no permission"; got != want {
		t.Fatalf("APIError.Message = %q, want %q", got, want)
	}
}

func TestAPIErrorFallsBackToBodySnippetForNonJSONError(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}))

	_, err := client.CreatePost(context.Background(), "channel-1", "", "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("CreatePost() error = %v, want an *APIError", err)
	}
	if !strings.Contains(apiErr.Message, "bad gateway") {
		t.Fatalf("APIError.Message = %q, want it to include the response snippet", apiErr.Message)
	}
}

func TestIsContentRejectedErrorOnlyMatchesContentRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "bad request status",
			err:  &APIError{StatusCode: http.StatusBadRequest},
			want: true,
		},
		{
			name: "bad request id on 200",
			err:  &APIError{StatusCode: http.StatusOK, ID: "BAD_REQUEST"},
			want: true,
		},
		{
			name: "internal server error is not content rejection",
			err:  &APIError{StatusCode: http.StatusInternalServerError},
			want: false,
		},
		{
			name: "forbidden is not content rejection",
			err:  &APIError{StatusCode: http.StatusForbidden},
			want: false,
		},
		{
			name: "transport error is not content rejection",
			err:  errors.New("dial tcp: connection refused"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContentRejectedError(tc.err); got != tc.want {
				t.Fatalf("isContentRejectedError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestUpdatePostUsesPutOnPostResource(t *testing.T) {
	var gotMethod, gotPath string
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Post{ID: "post-1"})
	}))

	if _, err := client.UpdatePost(context.Background(), "post-1", "edited"); err != nil {
		t.Fatalf("UpdatePost() error = %v", err)
	}
	if got, want := gotMethod, http.MethodPut; got != want {
		t.Fatalf("method = %q, want %q", got, want)
	}
	if got, want := gotPath, "/api/v4/posts/post-1"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestClientWithoutTokenFailsBeforeNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("request reached the server without a token")
	}))
	t.Cleanup(server.Close)

	client := NewClient(server.URL, "", "bot-1")
	if _, err := client.CreatePost(context.Background(), "channel-1", "", "hello"); err == nil {
		t.Fatal("CreatePost() error = nil, want a token-required error")
	}
}

func TestClientRespectsContextCancellation(t *testing.T) {
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.CreatePost(ctx, "channel-1", "", "hello"); err == nil {
		t.Fatal("CreatePost() error = nil, want a context error")
	}
}
