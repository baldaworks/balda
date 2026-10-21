package slackagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/rs/zerolog"
)

func newTestAdapter(client MessageClient, cfg AdapterConfig) *Adapter {
	return NewAdapter(client, nil, attachment.Limits{}, zerolog.Nop(), cfg)
}

const (
	testChatPostMessagePath = "/chat.postMessage"
	testHelloText           = "hello"
	testStreamMessageTS     = "1782234987.693923"
	testThreadTS            = "1782234671.392669"
)

func TestAdapterDeliverAgentReplyReturnsProviderMessageID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == testChatPostMessagePath {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"ts": "1712345678.000100",
			})
			return
		}
		if r.URL.Path != "/agents.sessions.setStatus" {
			t.Fatalf("unexpected path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(server.Close)

	client := NewClientWithBaseURL(server.URL, "xoxb-token")
	adapter := newTestAdapter(client, AdapterConfig{})
	locator := NewThreadLocator("T123", "C456", "thread-789")

	result, err := adapter.Deliver(t.Context(), locator, deliverycmd.Operation{
		Kind:    deliverycmd.OperationAgentReply,
		Message: &deliveryfmt.Message{Name: deliveryfmt.NameSlackMrkdwn, Text: testHelloText},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if got, want := result.ProviderMessageID, "1712345678.000100"; got != want {
		t.Fatalf("provider_message_id = %q, want %q", got, want)
	}
}

func TestAdapterDeliverAgentReplyAppendsSuggestedPromptsWhenEnabled(t *testing.T) {
	t.Parallel()

	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agents.sessions.setStatus" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if r.URL.Path != testChatPostMessagePath {
			t.Fatalf("unexpected path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"ts": "1712345678.000100",
		})
	}))
	t.Cleanup(server.Close)

	client := NewClientWithBaseURL(server.URL, "xoxb-token")
	adapter := newTestAdapter(client, AdapterConfig{SuggestedPrompts: true})
	locator := NewThreadLocator("T123", "D456", testThreadTS)

	_, err := adapter.Deliver(t.Context(), locator, deliverycmd.Operation{
		Kind: deliverycmd.OperationAgentReply,
		Text: testHelloText,
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	text, _ := got["text"].(string)
	if text == testHelloText || text == "" {
		t.Fatalf("request text = %q, want appended suggested prompts", text)
	}
}

func TestAdapterStreamsMonotonicSnapshotsAndFinalizesOnce(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{nextTS: testStreamMessageTS}
	adapter := newTestAdapter(client, AdapterConfig{EnableStreaming: true})
	locator := NewThreadLocator("T123", "D456", testThreadTS)

	for _, snapshot := range []string{"Hello", "Hello", "Hello world"} {
		_, err := adapter.Deliver(context.Background(), locator, deliverycmd.Operation{
			Kind: deliverycmd.OperationProgress,
			Progress: deliverycmd.Progress{
				Kind:    deliverycmd.ProgressThinking,
				Visible: true,
				Text:    snapshot,
			},
		})
		if err != nil {
			t.Fatalf("progress %q error = %v", snapshot, err)
		}
	}
	result, err := adapter.Deliver(context.Background(), locator, deliverycmd.Operation{
		Kind: deliverycmd.OperationAgentReply,
		Text: "Hello world!",
	})
	if err != nil {
		t.Fatalf("final delivery error = %v", err)
	}
	if result.ProviderMessageID != testStreamMessageTS {
		t.Fatalf("provider_message_id = %q", result.ProviderMessageID)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.starts) != 1 || client.starts[0].text != "Hello" {
		t.Fatalf("starts = %+v", client.starts)
	}
	if len(client.appends) != 2 || client.appends[0].text != " world" || client.appends[1].text != "!" {
		t.Fatalf("appends = %+v", client.appends)
	}
	if len(client.stops) != 1 || client.stops[0].text != "" || client.stops[0].status != SessionStatusActive {
		t.Fatalf("stops = %+v", client.stops)
	}
	if len(client.posts) != 0 {
		t.Fatalf("posts = %+v, want none", client.posts)
	}
}

func TestAdapterSendsTechnicalProgressAsVisibleText(t *testing.T) {
	t.Parallel()

	client := &recordingAgentClient{nextTS: "progress-ts"}
	adapter := newTestAdapter(client, AdapterConfig{})
	locator := NewThreadLocator("T123", "D456", testThreadTS)

	_, err := adapter.Deliver(t.Context(), locator, deliverycmd.Operation{
		Kind: deliverycmd.OperationProgress,
		Progress: deliverycmd.Progress{
			Kind:     deliverycmd.ProgressTechnical,
			Text:     "Working",
			Audience: deliverycmd.ProgressAudience{UserID: "user-42"},
			Visible:  true,
		},
	})
	if err != nil {
		t.Fatalf("SendProgress() error = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.posts) != 1 || client.posts[0].text != "Working" {
		t.Fatalf("posts = %+v, want one visible technical message", client.posts)
	}
}

func TestAdapterFinalOnlyStreamAndQuestionSuspension(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{nextTS: "stream-ts"}
	adapter := newTestAdapter(client, AdapterConfig{EnableStreaming: true})
	locator := NewThreadLocator("T123", "D456", "root-ts")
	result, err := adapter.Deliver(context.Background(), locator, deliverycmd.Operation{
		Kind:     deliverycmd.OperationAgentReply,
		Text:     "Need input",
		Question: &deliverycmd.Question{},
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if result.ProviderMessageID != "stream-ts" {
		t.Fatalf("provider_message_id = %q", result.ProviderMessageID)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.starts) != 1 || client.starts[0].text != "Need input" {
		t.Fatalf("starts = %+v", client.starts)
	}
	if len(client.stops) != 1 || client.stops[0].text != "" || client.stops[0].status != SessionStatusSuspended {
		t.Fatalf("stops = %+v", client.stops)
	}
}

func TestAdapterRejectsDivergentStreamWithoutFallbackPost(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{nextTS: "stream-ts"}
	adapter := newTestAdapter(client, AdapterConfig{EnableStreaming: true})
	locator := NewThreadLocator("T123", "D456", "root-ts")
	_, err := adapter.Deliver(context.Background(), locator, deliverycmd.Operation{
		Kind:     deliverycmd.OperationProgress,
		Progress: deliverycmd.Progress{Kind: deliverycmd.ProgressThinking, Visible: true, Text: "first"},
	})
	if err != nil {
		t.Fatalf("initial progress error = %v", err)
	}
	_, err = adapter.Deliver(context.Background(), locator, deliverycmd.Operation{
		Kind:     deliverycmd.OperationProgress,
		Progress: deliverycmd.Progress{Kind: deliverycmd.ProgressThinking, Visible: true, Text: "different"},
	})
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("divergent progress error = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.posts) != 0 || len(client.appends) != 0 || len(client.stops) != 0 {
		t.Fatalf("unexpected fallback side effects: posts=%+v appends=%+v stops=%+v", client.posts, client.appends, client.stops)
	}
}

func TestAdapterBeginTurnSetsProcessingAndRenamesOnlyOnce(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{}
	adapter := newTestAdapter(client, AdapterConfig{})
	locator := NewThreadLocator("T123", "D456", "root-ts")
	prompt := strings.Repeat("界", maxSessionTitleRunes+5)
	for range 2 {
		if err := adapter.BeginTurn(context.Background(), locator, "U123", prompt); err != nil {
			t.Fatalf("BeginTurn() error = %v", err)
		}
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.statuses) != 2 || client.statuses[0].Status != SessionStatusProcessing {
		t.Fatalf("statuses = %+v", client.statuses)
	}
	if len(client.renames) != 1 || utf8.RuneCountInString(client.renames[0].title) != maxSessionTitleRunes {
		t.Fatalf("renames = %+v", client.renames)
	}
}

func TestAdapterBeginTurnDoesNotRenameChannelThread(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{}
	adapter := newTestAdapter(client, AdapterConfig{})
	locator := NewThreadLocator("T123", "C456", "root-ts")

	for range 2 {
		if err := adapter.BeginTurn(context.Background(), locator, "U123", "<@UBOT> hello"); err != nil {
			t.Fatalf("BeginTurn() error = %v", err)
		}
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.statuses) != 2 {
		t.Fatalf("statuses = %+v, want two processing updates", client.statuses)
	}
	if len(client.renames) != 0 {
		t.Fatalf("renames = %+v, want no channel rename", client.renames)
	}
}

func TestAdapterStreamsDistinctSessionsConcurrently(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{nextTS: "stream-ts"}
	adapter := newTestAdapter(client, AdapterConfig{EnableStreaming: true})
	locators := []deliverycmd.Locator{
		NewThreadLocator("T123", "D456", "root-a"),
		NewThreadLocator("T123", "D456", "root-b"),
	}
	var wg sync.WaitGroup
	for i, locator := range locators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text := fmt.Sprintf("response-%d", i)
			if _, err := adapter.Deliver(context.Background(), locator, deliverycmd.Operation{
				Kind:     deliverycmd.OperationProgress,
				Progress: deliverycmd.Progress{Kind: deliverycmd.ProgressThinking, Visible: true, Text: text},
			}); err != nil {
				t.Errorf("progress error = %v", err)
			}
			if _, err := adapter.Deliver(context.Background(), locator, deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Text: text}); err != nil {
				t.Errorf("final error = %v", err)
			}
		}()
	}
	wg.Wait()
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.starts) != 2 || len(client.stops) != 2 {
		t.Fatalf("starts=%d stops=%d", len(client.starts), len(client.stops))
	}
}

func TestAdapterStoppedSessionClearsStreamAndSetsActive(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{nextTS: "stream-ts"}
	adapter := newTestAdapter(client, AdapterConfig{EnableStreaming: true})
	locator := NewThreadLocator("T123", "D456", "root-ts")
	progress := deliverycmd.Operation{
		Kind:     deliverycmd.OperationProgress,
		Progress: deliverycmd.Progress{Kind: deliverycmd.ProgressThinking, Visible: true, Text: "partial"},
	}
	if _, err := adapter.Deliver(context.Background(), locator, progress); err != nil {
		t.Fatalf("initial progress error = %v", err)
	}
	if err := adapter.HandleSessionStopped(context.Background(), locator); err != nil {
		t.Fatalf("HandleSessionStopped() error = %v", err)
	}
	progress.Progress.Text = "new response"
	if _, err := adapter.Deliver(context.Background(), locator, progress); err != nil {
		t.Fatalf("new progress error = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.statuses) != 1 || client.statuses[0].Status != SessionStatusActive {
		t.Fatalf("statuses = %+v", client.statuses)
	}
	if len(client.starts) != 2 {
		t.Fatalf("starts = %+v, want a fresh stream", client.starts)
	}
}

func TestAdapterCloseSessionSetsClosed(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{}
	adapter := newTestAdapter(client, AdapterConfig{})
	locator := NewThreadLocator("T123", "D456", "root-ts")
	if err := adapter.CloseSession(context.Background(), locator); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.statuses) != 1 || client.statuses[0].Status != SessionStatusClosed {
		t.Fatalf("statuses = %+v", client.statuses)
	}
}

func TestAdapterRetriesStatusWithoutReplayingSuccessfulPost(t *testing.T) {
	t.Parallel()
	client := &recordingAgentClient{nextTS: "message-ts", statusErr: errors.New("status unavailable")}
	adapter := newTestAdapter(client, AdapterConfig{})
	locator := NewThreadLocator("T123", "D456", "root-ts")
	operation := deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Text: "done"}
	if _, err := adapter.Deliver(context.Background(), locator, operation); err == nil {
		t.Fatal("first Deliver() error = nil, want status failure")
	}
	client.mu.Lock()
	client.statusErr = nil
	client.mu.Unlock()
	result, err := adapter.Deliver(context.Background(), locator, operation)
	if err != nil {
		t.Fatalf("retry Deliver() error = %v", err)
	}
	if result.ProviderMessageID != "message-ts" {
		t.Fatalf("provider_message_id = %q", result.ProviderMessageID)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.posts) != 1 || len(client.statuses) != 2 {
		t.Fatalf("posts=%d statuses=%d", len(client.posts), len(client.statuses))
	}
}

type deliveryCall struct {
	channel string
	thread  string
	ts      string
	text    string
	status  SessionStatus
}

type renameCall struct {
	channel string
	thread  string
	title   string
}

type recordingAgentClient struct {
	mu        sync.Mutex
	nextTS    string
	statusErr error
	posts     []deliveryCall
	starts    []deliveryCall
	appends   []deliveryCall
	stops     []deliveryCall
	statuses  []SetSessionStatusRequest
	renames   []renameCall
}

func (c *recordingAgentClient) PostMessage(_ context.Context, channel, threadTS, text string, _ bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.posts = append(c.posts, deliveryCall{channel: channel, thread: threadTS, text: text})
	return c.nextTS, nil
}

func (c *recordingAgentClient) SetSessionStatus(_ context.Context, input SetSessionStatusRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statuses = append(c.statuses, input)
	return c.statusErr
}

func (c *recordingAgentClient) RenameSession(_ context.Context, channelID, threadTS, title string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renames = append(c.renames, renameCall{channel: channelID, thread: threadTS, title: title})
	return nil
}

func (c *recordingAgentClient) StartStream(_ context.Context, channel, threadTS, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starts = append(c.starts, deliveryCall{channel: channel, thread: threadTS, text: text})
	return c.nextTS, nil
}

func (c *recordingAgentClient) AppendStream(_ context.Context, channel, ts, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appends = append(c.appends, deliveryCall{channel: channel, ts: ts, text: text})
	return nil
}

func (c *recordingAgentClient) StopStream(_ context.Context, channel, ts, text string, status SessionStatus) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stops = append(c.stops, deliveryCall{channel: channel, ts: ts, text: text, status: status})
	return nil
}

func TestAdapterLiteralFinalPreservesJSONTextAfterStreaming(t *testing.T) {
	const source = "**partial**\n\nProvider error:\nМодель & < > &amp; &lt; &gt; <@synthetic> <https://example.com|label>"
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming-%t", streaming), func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			var final map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				mu.Lock()
				paths = append(paths, r.URL.Path)
				if r.URL.Path == testChatPostMessagePath {
					final = body
				}
				mu.Unlock()
				ts := "stream-ts"
				if r.URL.Path == testChatPostMessagePath {
					ts = "final-ts"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": ts})
			}))
			t.Cleanup(server.Close)
			adapter := newTestAdapter(NewClientWithBaseURL(server.URL, "synthetic-token"), AdapterConfig{EnableStreaming: streaming})
			locator := NewThreadLocator("T123", "D456", testThreadTS)
			if streaming {
				if _, err := adapter.Deliver(t.Context(), locator, deliverycmd.Operation{Kind: deliverycmd.OperationProgress, Progress: deliverycmd.Progress{Kind: deliverycmd.ProgressThinking, Visible: true, Text: "**partial**"}}); err != nil {
					t.Fatal(err)
				}
			}
			rendered := presentation.RenderTurnFailure(deliveryfmt.TurnFailure{Text: source})
			result, err := adapter.Deliver(t.Context(), locator, deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Message: &deliveryfmt.Message{Name: deliveryfmt.NamePlainText, Text: rendered}})
			if err != nil {
				t.Fatalf("literal final error=%v", err)
			}
			if result.ProviderMessageID != "final-ts" {
				t.Fatalf("message ID=%q", result.ProviderMessageID)
			}
			mu.Lock()
			defer mu.Unlock()
			want := "/chat.postMessage,/agents.sessions.setStatus"
			if streaming {
				want = "/chat.startStream,/chat.stopStream," + want
			}
			if got := strings.Join(paths, ","); got != want {
				t.Fatalf("request sequence=%q, want %q", got, want)
			}
			wire, _ := final["text"].(string)
			mrkdwn, ok := final["mrkdwn"].(bool)
			if !ok || mrkdwn {
				t.Fatalf("plain final mrkdwn=%v", final["mrkdwn"])
			}
			display := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(wire)
			if display != source {
				t.Fatalf("display=%q, want %q (wire %q)", display, source, wire)
			}
		})
	}
}

type literalFinalFailureClient struct {
	*recordingAgentClient
	fail  string
	cause error
	order []string
}

func (c *literalFinalFailureClient) StopStream(ctx context.Context, channel, ts, text string, status SessionStatus) error {
	c.order = append(c.order, "stop")
	if c.fail == "stop" {
		return c.cause
	}
	return c.recordingAgentClient.StopStream(ctx, channel, ts, text, status)
}
func (c *literalFinalFailureClient) PostMessage(ctx context.Context, channel, thread, text string, mrkdwn bool) (string, error) {
	c.order = append(c.order, "post")
	if c.fail == "post" {
		return "", c.cause
	}
	return c.recordingAgentClient.PostMessage(ctx, channel, thread, text, mrkdwn)
}
func (c *literalFinalFailureClient) SetSessionStatus(ctx context.Context, input SetSessionStatusRequest) error {
	c.order = append(c.order, "status")
	if c.fail == "status" {
		return c.cause
	}
	return c.recordingAgentClient.SetSessionStatus(ctx, input)
}
func TestAdapterLiteralFinalPropagatesStopPostAndStatusFailures(t *testing.T) {
	for _, failure := range []string{"stop", "post", "status"} {
		t.Run(failure, func(t *testing.T) {
			cause := errors.New("synthetic Slack failure")
			client := &literalFinalFailureClient{recordingAgentClient: &recordingAgentClient{nextTS: "message-ts"}, fail: failure, cause: cause}
			adapter := newTestAdapter(client, AdapterConfig{EnableStreaming: true})
			locator := NewThreadLocator("T123", "D456", testThreadTS)
			if _, err := adapter.Deliver(t.Context(), locator, deliverycmd.Operation{Kind: deliverycmd.OperationProgress, Progress: deliverycmd.Progress{Kind: deliverycmd.ProgressThinking, Visible: true, Text: "partial"}}); err != nil {
				t.Fatal(err)
			}
			op := deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Message: &deliveryfmt.Message{Name: deliveryfmt.NamePlainText, Text: "partial\nProvider error: busy"}, Question: &deliverycmd.Question{}}
			result, err := adapter.Deliver(t.Context(), locator, op)
			if !errors.Is(err, cause) || result.ProviderMessageID != "" {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			want := map[string]string{"stop": "stop", "post": "stop,post", "status": "stop,post,status"}[failure]
			if got := strings.Join(client.order, ","); got != want {
				t.Fatalf("order=%q, want %q", got, want)
			}
			if len(client.appends) != 0 {
				t.Fatalf("raw final appended to stream: %+v", client.appends)
			}
			if failure == "status" {
				client.fail = ""
				result, err = adapter.Deliver(t.Context(), locator, op)
				if err != nil || result.ProviderMessageID != "message-ts" || len(client.posts) != 1 {
					t.Fatalf("retry result=%+v error=%v posts=%d", result, err, len(client.posts))
				}
				if len(client.statuses) == 0 || client.statuses[len(client.statuses)-1].Status != SessionStatusSuspended {
					t.Fatalf("statuses=%+v", client.statuses)
				}
			}
		})
	}
}
