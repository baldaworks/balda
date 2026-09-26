package mattermost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/rs/zerolog"
)

// createPostRequest is the decoded body the adapter sends to POST /api/v4/posts.
type createPostRequest struct {
	ChannelID string `json:"channel_id"`
	RootID    string `json:"root_id"`
	Message   string `json:"message"`
}

// fakeServer emulates the Mattermost REST endpoints the adapter uses and records
// every create-post request so tests can assert on channel, thread and content.
type fakeServer struct {
	mu       sync.Mutex
	requests []createPostRequest
	// respond is called for each create-post request; when nil the server
	// answers 201 with a generated post.
	respond func(attempt int, req createPostRequest) (status int, body string)
}

func (f *fakeServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/posts") && r.Method == http.MethodPost:
			var req createPostRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"id":"api.json.decode","message":"bad body"}`))
				return
			}
			f.mu.Lock()
			f.requests = append(f.requests, req)
			attempt := len(f.requests)
			respond := f.respond
			f.mu.Unlock()

			if respond != nil {
				status, body := respond(attempt, req)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Post{ID: "post-created-1", ChannelID: req.ChannelID, RootID: req.RootID, Message: req.Message})
		case strings.HasSuffix(r.URL.Path, "/posts") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "post-1", "message": "original"})
		case strings.HasSuffix(r.URL.Path, "/posts/"+lastSegment(r.URL.Path)) && r.Method == http.MethodPut:
			// PUT /posts/{id} — edit in place.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(Post{ID: lastSegment(r.URL.Path)})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"id":"api.context.404.app_error","message":"not found"}`))
		}
	})
}

func lastSegment(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return trimmed[idx+1:]
	}
	return trimmed
}

func (f *fakeServer) recorded() []createPostRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]createPostRequest(nil), f.requests...)
}

func newTestAdapter(t *testing.T, fake *fakeServer) *Adapter {
	t.Helper()
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	return NewAdapter(NewClient(server.URL, "bot-token", "bot-1"), zerolog.Nop())
}

func TestSendPlainPostsToChannel(t *testing.T) {
	fake := &fakeServer{}
	adapter := newTestAdapter(t, fake)

	if err := adapter.SendPlain(context.Background(), NewChannelLocator(testTeamID, testChannelID, ""), "hello"); err != nil {
		t.Fatalf("SendPlain() error = %v", err)
	}

	requests := fake.recorded()
	if len(requests) != 1 {
		t.Fatalf("create post called %d times, want 1", len(requests))
	}
	if got, want := requests[0].ChannelID, testChannelID; got != want {
		t.Fatalf("channel_id = %q, want %q", got, want)
	}
	if got := requests[0].RootID; got != "" {
		t.Fatalf("root_id = %q, want \"\" for a channel-level post", got)
	}
	if got, want := requests[0].Message, "hello"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestSendPlainPostsIntoThreadRoot(t *testing.T) {
	fake := &fakeServer{}
	adapter := newTestAdapter(t, fake)

	locator := NewChannelLocator(testTeamID, testChannelID, "root-1")
	if err := adapter.SendPlain(context.Background(), locator, "thread reply"); err != nil {
		t.Fatalf("SendPlain() error = %v", err)
	}

	requests := fake.recorded()
	if len(requests) != 1 {
		t.Fatalf("create post called %d times, want 1", len(requests))
	}
	if got, want := requests[0].RootID, "root-1"; got != want {
		t.Fatalf("root_id = %q, want %q so the reply lands in the thread", got, want)
	}
	if got, want := requests[0].ChannelID, testChannelID; got != want {
		t.Fatalf("channel_id = %q, want %q", got, want)
	}
}

func TestSendPlainPostsToDirectChannel(t *testing.T) {
	fake := &fakeServer{}
	adapter := newTestAdapter(t, fake)

	if err := adapter.SendPlain(context.Background(), NewDMLocator(testDMChannelID, testUserID), "hi"); err != nil {
		t.Fatalf("SendPlain() error = %v", err)
	}

	requests := fake.recorded()
	if len(requests) != 1 {
		t.Fatalf("create post called %d times, want 1", len(requests))
	}
	if got, want := requests[0].ChannelID, testDMChannelID; got != want {
		t.Fatalf("channel_id = %q, want %q", got, want)
	}
}

func TestSendAgentReplyFallsBackToPlainTextOnContentRejection(t *testing.T) {
	// Mattermost rejects some markdown (for example a broken image link) with a
	// 400. The adapter must retry the same logical message as plain text so the
	// user still receives it.
	fake := &fakeServer{
		respond: func(attempt int, req createPostRequest) (int, string) {
			if attempt == 1 {
				return http.StatusBadRequest, `{"id":"api.post.create_post.bad_request","message":"bad image link"}`
			}
			return http.StatusCreated, `{"id":"post-fallback-1"}`
		},
	}
	adapter := newTestAdapter(t, fake)

	providerMessageID, err := adapter.SendAgentReplyWithProviderMessageID(
		context.Background(),
		NewChannelLocator(testTeamID, testChannelID, ""),
		"Screenshot: ![broken](https://example.invalid/missing.png)",
	)
	if err != nil {
		t.Fatalf("SendAgentReplyWithProviderMessageID() error = %v", err)
	}
	if got, want := providerMessageID, "post-fallback-1"; got != want {
		t.Fatalf("providerMessageID = %q, want %q from the successful fallback", got, want)
	}

	requests := fake.recorded()
	if len(requests) != 2 {
		t.Fatalf("create post called %d times, want 2 (original + plain fallback)", len(requests))
	}
	if requests[0].Message == requests[1].Message {
		t.Fatal("fallback request repeated the rejected markdown verbatim; want degraded plain text")
	}
	if strings.Contains(requests[1].Message, "![") {
		t.Fatalf("fallback message %q still contains markdown image syntax", requests[1].Message)
	}
}

func TestSendAgentReplyDoesNotFallBackOnServerError(t *testing.T) {
	// Only content rejection may trigger a retry: retrying a 5xx would post the
	// same reply twice once the server recovers.
	fake := &fakeServer{
		respond: func(int, createPostRequest) (int, string) {
			return http.StatusInternalServerError, `{"id":"api.context.internal","message":"boom"}`
		},
	}
	adapter := newTestAdapter(t, fake)

	_, err := adapter.SendAgentReplyWithProviderMessageID(
		context.Background(),
		NewChannelLocator(testTeamID, testChannelID, ""),
		"![img](https://example.invalid/x.png)",
	)
	if err == nil {
		t.Fatal("SendAgentReplyWithProviderMessageID() error = nil, want the server error")
	}

	if requests := fake.recorded(); len(requests) != 1 {
		t.Fatalf("create post called %d times, want exactly 1 — a 5xx must not be retried", len(requests))
	}
}

func TestDeliverPlainOperation(t *testing.T) {
	fake := &fakeServer{}
	adapter := newTestAdapter(t, fake)

	result, err := adapter.Deliver(context.Background(), NewChannelLocator(testTeamID, testChannelID, ""), deliverycmd.Operation{
		Kind: deliverycmd.OperationPlain,
		Text: "ping",
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if result.ProviderMessageID != "" {
		t.Fatalf("result.ProviderMessageID = %q, want \"\" for a plain send", result.ProviderMessageID)
	}
	if requests := fake.recorded(); len(requests) != 1 {
		t.Fatalf("create post called %d times, want 1", len(requests))
	}
}

func TestDeliverAgentReplyReportsProviderMessageID(t *testing.T) {
	fake := &fakeServer{}
	adapter := newTestAdapter(t, fake)

	result, err := adapter.Deliver(context.Background(), NewChannelLocator(testTeamID, testChannelID, ""), deliverycmd.Operation{
		Kind: deliverycmd.OperationAgentReply,
		Text: "answer",
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if got, want := result.ProviderMessageID, "post-created-1"; got != want {
		t.Fatalf("result.ProviderMessageID = %q, want %q", got, want)
	}
}

func TestDeliverDraftRendersAsPostInsteadOfDropping(t *testing.T) {
	// Mattermost has no draft concept. The draft text must still reach the user
	// rather than being silently discarded.
	fake := &fakeServer{}
	adapter := newTestAdapter(t, fake)

	if _, err := adapter.Deliver(context.Background(), NewChannelLocator(testTeamID, testChannelID, ""), deliverycmd.Operation{
		Kind: deliverycmd.OperationDraft,
		Text: "draft body",
	}); err != nil {
		t.Fatalf("Deliver(draft) error = %v", err)
	}

	requests := fake.recorded()
	if len(requests) != 1 {
		t.Fatalf("create post called %d times, want 1", len(requests))
	}
	if got, want := requests[0].Message, "draft body"; got != want {
		t.Fatalf("draft message = %q, want %q", got, want)
	}
}

func TestDeliverTypingIsNoOpWithoutNetworkCall(t *testing.T) {
	// Mattermost bot accounts have no typing API, so typing must be a local
	// no-op and never produce a request.
	fake := &fakeServer{}
	adapter := newTestAdapter(t, fake)

	if _, err := adapter.Deliver(context.Background(), NewChannelLocator(testTeamID, testChannelID, ""), deliverycmd.Operation{
		Kind: deliverycmd.OperationTyping,
	}); err != nil {
		t.Fatalf("Deliver(typing) error = %v", err)
	}

	if requests := fake.recorded(); len(requests) != 0 {
		t.Fatalf("typing produced %d network calls, want 0", len(requests))
	}
}

func TestDeliverRejectsUnsupportedOperation(t *testing.T) {
	adapter := newTestAdapter(t, &fakeServer{})

	_, err := adapter.Deliver(context.Background(), NewChannelLocator(testTeamID, testChannelID, ""), deliverycmd.Operation{
		Kind: deliverycmd.OperationKind("nonsense"),
	})
	if err == nil {
		t.Fatal("Deliver(unsupported) error = nil, want an error")
	}
}

func TestDeliverPhotoWithoutMediaFails(t *testing.T) {
	adapter := newTestAdapter(t, &fakeServer{})

	_, err := adapter.Deliver(context.Background(), NewChannelLocator(testTeamID, testChannelID, ""), deliverycmd.Operation{
		Kind: deliverycmd.OperationPhoto,
	})
	if err == nil {
		t.Fatal("Deliver(photo without media) error = nil, want an error")
	}
}

func TestAdapterWithoutClientFailsLoudly(t *testing.T) {
	adapter := NewAdapter(nil, zerolog.Nop())

	err := adapter.SendPlain(context.Background(), NewChannelLocator(testTeamID, testChannelID, ""), "hello")
	if err == nil {
		t.Fatal("SendPlain() error = nil, want a client-required error")
	}
	if !strings.Contains(err.Error(), "client is required") {
		t.Fatalf("SendPlain() error = %v, want a message naming the missing client", err)
	}
}

func TestAdapterRejectsForeignLocator(t *testing.T) {
	adapter := newTestAdapter(t, &fakeServer{})

	err := adapter.SendPlain(context.Background(), deliverycmd.Locator{ChannelType: "zulip"}, "hello")
	if err == nil {
		t.Fatal("SendPlain(zulip locator) error = nil, want an unsupported channel error")
	}
}

func TestMarkdownPlainTextDegradesMarkdown(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "image becomes a link plus alt text",
			in:   "![alt](https://example.com/img.png)",
			want: "alt: https://example.com/img.png",
		},
		{
			name: "link becomes text plus url",
			in:   "[docs](https://example.com/docs)",
			want: "docs (https://example.com/docs)",
		},
		{
			name: "emphasis markers are stripped",
			in:   "**bold** and __underline__ and `code`",
			want: "bold and underline and code",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MarkdownPlainText(tc.in); got != tc.want {
				t.Fatalf("MarkdownPlainText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAdapterImplementsDeliveryContract(t *testing.T) {
	var _ deliverycmd.Adapter = (*Adapter)(nil)
}

func TestDeliverUndecodableLocatorFails(t *testing.T) {
	adapter := newTestAdapter(t, &fakeServer{})

	locator := NewChannelLocator(testTeamID, testChannelID, "")
	locator.AddressJSON = "{"

	err := adapter.SendPlain(context.Background(), locator, "hello")
	if err == nil {
		t.Fatal("SendPlain() error = nil, want a locator decode error")
	}
	if !strings.Contains(err.Error(), "decode mattermost locator") {
		t.Fatalf("SendPlain() error = %v, want a locator decode error", err)
	}
}

func TestSendMarkdownWithFormatUsesMarkdownFallback(t *testing.T) {
	fake := &fakeServer{
		respond: func(attempt int, req createPostRequest) (int, string) {
			if attempt == 1 {
				return http.StatusBadRequest, `{"id":"api.post.create_post.bad_request","message":"rejected"}`
			}
			return http.StatusCreated, `{"id":"post-2"}`
		},
	}
	adapter := newTestAdapter(t, fake)

	if err := adapter.SendMarkdownWithFormat(
		context.Background(),
		NewChannelLocator(testTeamID, testChannelID, ""),
		deliveryfmt.DeliveryFormatMarkdown,
		"**bold**",
	); err != nil {
		t.Fatalf("SendMarkdownWithFormat() error = %v", err)
	}

	if requests := fake.recorded(); len(requests) != 2 {
		t.Fatalf("create post called %d times, want 2 after content rejection", len(requests))
	}
}
