package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// recordingProcessor captures everything the ingress hands to the processor.
type recordingProcessor struct {
	mu            sync.Mutex
	inbound       []InboundMessage
	commands      []InboundCommand
	unsupported   []InboundCommand
	inboundResult turncmd.InboundSettlement
	inboundErr    error
	inboundDone   chan struct{}
}

func (p *recordingProcessor) ProcessInbound(_ context.Context, msg InboundMessage) (turncmd.InboundSettlement, error) {
	p.mu.Lock()
	p.inbound = append(p.inbound, msg)
	p.mu.Unlock()
	if p.inboundDone != nil {
		select {
		case p.inboundDone <- struct{}{}:
		default:
		}
	}
	return p.inboundResult, p.inboundErr
}

func (p *recordingProcessor) HandleCommand(_ context.Context, cmd InboundCommand) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commands = append(p.commands, cmd)
	return nil
}

func (p *recordingProcessor) HandleUnsupportedCommand(_ context.Context, cmd InboundCommand) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unsupported = append(p.unsupported, cmd)
	return nil
}

func (p *recordingProcessor) inboundMessages() []InboundMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]InboundMessage(nil), p.inbound...)
}

func (p *recordingProcessor) commandInvocations() []InboundCommand {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]InboundCommand(nil), p.commands...)
}

func (p *recordingProcessor) unsupportedInvocations() []InboundCommand {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]InboundCommand(nil), p.unsupported...)
}

// postedEventPayload builds a websocket "posted" frame whose data payload mirrors
// Mattermost: the post itself is a JSON-encoded string under "post".
func postedEventPayload(t *testing.T, post Post, channelType, senderName string) []byte {
	t.Helper()
	postJSON, err := json.Marshal(post)
	if err != nil {
		t.Fatalf("marshal post: %v", err)
	}
	data, err := json.Marshal(map[string]any{
		"channel_type": channelType,
		"team_id":      "team-1",
		"sender_name":  senderName,
		"post":         string(postJSON),
	})
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	frame, err := json.Marshal(map[string]any{"event": eventPosted, "data": json.RawMessage(data)})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return frame
}

func newTestIngress(processor InboundProcessor, botUserID, botUsername string) *Ingress {
	return NewIngress(IngressParams{
		Processor:   processor,
		Client:      NewClient("http://localhost:8065", "token", botUserID),
		Commands:    nil,
		Enabled:     true,
		BotUserID:   botUserID,
		BotUsername: botUsername,
		Logger:      zerolog.Nop(),
	})
}

func TestHandleFrameDispatchesChannelMentionToProcessor(t *testing.T) {
	processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
	ingress := newTestIngress(processor, "bot-1", "balda")

	frame := postedEventPayload(t, Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "channel-1",
		Message:   "@balda reply with a single word",
	}, channelTypeOpen, "Alice")

	ingress.handleFrame(context.Background(), frame)

	select {
	case <-processor.inboundDone:
	case <-time.After(2 * time.Second):
		t.Fatal("processor.ProcessInbound was not called for a channel mention")
	}

	messages := processor.inboundMessages()
	if len(messages) != 1 {
		t.Fatalf("ProcessInbound called %d times, want 1", len(messages))
	}
	if got, want := messages[0].Text, "reply with a single word"; got != want {
		t.Fatalf("inbound text = %q, want the mention stripped: %q", got, want)
	}
	if got, want := messages[0].SenderID, "user-1"; got != want {
		t.Fatalf("inbound sender = %q, want %q", got, want)
	}
	if messages[0].Direct {
		t.Fatal("inbound Direct = true, want false for a team channel")
	}
}

func TestHandleFrameIgnoresChannelPostWithoutMention(t *testing.T) {
	processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
	ingress := newTestIngress(processor, "bot-1", "balda")

	frame := postedEventPayload(t, Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "channel-1",
		Message:   "just chatting",
	}, channelTypeOpen, "Alice")

	ingress.handleFrame(context.Background(), frame)

	select {
	case <-processor.inboundDone:
		t.Fatal("an unmentioned channel post must not reach the processor")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHandleFrameProcessesDirectMessageWithoutMention(t *testing.T) {
	processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
	ingress := newTestIngress(processor, "bot-1", "balda")

	frame := postedEventPayload(t, Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "dm-channel-1",
		Message:   "hello there",
	}, channelTypeDirect, "Alice")

	ingress.handleFrame(context.Background(), frame)

	select {
	case <-processor.inboundDone:
	case <-time.After(2 * time.Second):
		t.Fatal("processor.ProcessInbound was not called for a direct message")
	}
	messages := processor.inboundMessages()
	if len(messages) != 1 || !messages[0].Direct {
		t.Fatalf("inbound messages = %+v, want one direct message", messages)
	}
}

func TestHandleFrameDropsBotEcho(t *testing.T) {
	processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
	ingress := newTestIngress(processor, "bot-1", "balda")

	frame := postedEventPayload(t, Post{
		ID:        "post-1",
		UserID:    "bot-1",
		ChannelID: "channel-1",
		Message:   "@balda echo",
	}, channelTypeOpen, "balda")

	ingress.handleFrame(context.Background(), frame)

	select {
	case <-processor.inboundDone:
		t.Fatal("the bot's own post must not be processed back")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHandleFrameDropsSystemAndDeletedPosts(t *testing.T) {
	cases := map[string]Post{
		"system post":  {ID: "post-1", UserID: "user-1", ChannelID: "channel-1", Message: "@balda hi", Type: "system_join_channel"},
		"deleted post": {ID: "post-1", UserID: "user-1", ChannelID: "channel-1", Message: "@balda hi", DeleteAt: 1},
	}
	for name, post := range cases {
		t.Run(name, func(t *testing.T) {
			processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
			ingress := newTestIngress(processor, "bot-1", "balda")

			ingress.handleFrame(context.Background(), postedEventPayload(t, post, channelTypeOpen, "Alice"))

			select {
			case <-processor.inboundDone:
				t.Fatalf("%s must not reach the processor", name)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

func TestHandleFrameRoutesCommandToCommandHandler(t *testing.T) {
	processor := &recordingProcessor{}
	ingress := newTestIngress(processor, "bot-1", "balda")

	frame := postedEventPayload(t, Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "dm-channel-1",
		Message:   "/topic ops",
	}, channelTypeDirect, "Alice")

	ingress.handleFrame(context.Background(), frame)

	deadline := time.After(2 * time.Second)
	for {
		if commands := processor.commandInvocations(); len(commands) == 1 {
			if got, want := commands[0].Command, "topic"; got != want {
				t.Fatalf("command = %q, want %q", got, want)
			}
			if got, want := commands[0].Args, "ops"; got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("HandleCommand was not called; invocations = %+v", processor.commandInvocations())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestHandleFrameRoutesUnsupportedCommandToItsHandler(t *testing.T) {
	processor := &recordingProcessor{}
	ingress := newTestIngress(processor, "bot-1", "balda")

	frame := postedEventPayload(t, Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "dm-channel-1",
		Message:   "/nonsense",
	}, channelTypeDirect, "Alice")

	ingress.handleFrame(context.Background(), frame)

	deadline := time.After(2 * time.Second)
	for {
		if unsupported := processor.unsupportedInvocations(); len(unsupported) == 1 {
			if got, want := unsupported[0].Command, "nonsense"; got != want {
				t.Fatalf("unsupported command = %q, want %q", got, want)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("HandleUnsupportedCommand was not called; invocations = %+v", processor.unsupportedInvocations())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestHandleFrameIgnoresEditedAndDeletedEvents(t *testing.T) {
	for _, event := range []string{eventPostEdited, eventPostDeleted} {
		t.Run(event, func(t *testing.T) {
			processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
			ingress := newTestIngress(processor, "bot-1", "balda")

			frame, err := json.Marshal(map[string]any{
				"event": event,
				"data":  json.RawMessage(`{"post":"{\"id\":\"post-1\",\"user_id\":\"user-1\",\"channel_id\":\"channel-1\",\"message\":\"@balda hi\"}"}`),
			})
			if err != nil {
				t.Fatalf("marshal frame: %v", err)
			}

			ingress.handleFrame(context.Background(), frame)

			select {
			case <-processor.inboundDone:
				t.Fatalf("%s must not trigger processing", event)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}

func TestHandleFrameLogsRejectedClientFrameWithoutDispatching(t *testing.T) {
	processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
	ingress := newTestIngress(processor, "bot-1", "balda")

	// Mirrors the real "bad_seq" rejection Mattermost sends for a client frame
	// that omits or mis-orders seq.
	frame := []byte(`{"status":"FAIL","error":{"id":"api.web_socket_router.bad_seq.app_error","message":"Invalid sequence for WebSocket message."}}`)

	ingress.handleFrame(context.Background(), frame)

	select {
	case <-processor.inboundDone:
		t.Fatal("a FAIL status frame must not dispatch work")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestHandleFrameToleratesUndecodablePayload(t *testing.T) {
	processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
	ingress := newTestIngress(processor, "bot-1", "balda")

	for _, frame := range [][]byte{
		[]byte(`not-json`),
		[]byte(`{"event":"posted","data":"not-an-object"}`),
		[]byte(`{"event":"posted","data":{"post":"not-json"}}`),
		[]byte(`{"event":"posted","data":{"post":""}}`),
	} {
		ingress.handleFrame(context.Background(), frame)
	}

	select {
	case <-processor.inboundDone:
		t.Fatal("malformed frames must not dispatch work")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestProcessPostedRecoversFromProcessorPanic(t *testing.T) {
	// A panicking processor must not take down the ingress goroutine.
	ingress := newTestIngress(panickingProcessor{}, "bot-1", "balda")

	done := make(chan struct{})
	go func() {
		defer close(done)
		ingress.processPosted(context.Background(), PostedData{ChannelType: channelTypeDirect}, Post{
			ID:        "post-1",
			UserID:    "user-1",
			ChannelID: "dm-channel-1",
			Message:   "hello",
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("processPosted did not return; the panic was not recovered")
	}
}

type panickingProcessor struct{}

func (panickingProcessor) ProcessInbound(context.Context, InboundMessage) (turncmd.InboundSettlement, error) {
	panic("boom")
}

func (panickingProcessor) HandleCommand(context.Context, InboundCommand) error { return nil }

func (panickingProcessor) HandleUnsupportedCommand(context.Context, InboundCommand) error {
	return nil
}

func TestProcessPostedSkipsDispatchWithoutProcessor(t *testing.T) {
	// The processor is the single required dependency. When it is absent the
	// ingress must not panic: it simply has nothing to hand the post to.
	ingress := newTestIngress(nil, "bot-1", "balda")

	ingress.processPosted(context.Background(), PostedData{ChannelType: channelTypeDirect}, Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "dm-channel-1",
		Message:   "hello",
	})
}

func TestNextSeqIsMonotonicAndStartsAboveZero(t *testing.T) {
	ingress := newTestIngress(nil, "bot-1", "balda")

	// Mattermost rejects a client frame whose seq is not strictly increasing, so
	// the first seq must be >= 1 and every subsequent value must grow.
	first := ingress.nextSeq()
	if first < 1 {
		t.Fatalf("first nextSeq() = %d, want >= 1", first)
	}
	previous := first
	for i := 0; i < 100; i++ {
		current := ingress.nextSeq()
		if current <= previous {
			t.Fatalf("nextSeq() = %d after %d, want strictly increasing", current, previous)
		}
		previous = current
	}
}

func TestNextSeqIsSafeForConcurrentUse(t *testing.T) {
	ingress := newTestIngress(nil, "bot-1", "balda")

	const workers = 16
	const perWorker = 50
	var wg sync.WaitGroup
	results := make(chan int64, workers*perWorker)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				results <- ingress.nextSeq()
			}
		}()
	}
	wg.Wait()
	close(results)

	// Concurrent sequence allocation must never hand out a duplicate, otherwise
	// Mattermost sees a reordered frame and kills the subscription.
	seen := make(map[int64]bool, workers*perWorker)
	for value := range results {
		if seen[value] {
			t.Fatalf("nextSeq() returned a duplicate value %d", value)
		}
		seen[value] = true
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("allocated %d distinct seq values, want %d", len(seen), workers*perWorker)
	}
	if _, ok := seen[0]; ok {
		t.Fatal("nextSeq() returned 0, which Mattermost rejects as an invalid sequence")
	}
}

func TestWriteEventAlwaysIncludesSeqAndAction(t *testing.T) {
	// This is the regression guard for the "bad_seq" defect: Mattermost stops
	// pushing events (without closing the socket) if any client frame omits seq.
	upgrader := websocket.Upgrader{}
	received := make(chan map[string]any, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			var frame map[string]any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			received <- frame
		}
	}))
	t.Cleanup(server.Close)

	client, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ingress := newTestIngress(nil, "bot-1", "balda")
	if err := ingress.writeEvent(client, websocketActionPing, nil); err != nil {
		t.Fatalf("writeEvent(ping) error = %v", err)
	}
	if err := ingress.writeEvent(client, "custom_action", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("writeEvent(custom) error = %v", err)
	}

	var previous float64
	for i := 0; i < 2; i++ {
		select {
		case frame := <-received:
			seq, ok := frame["seq"].(float64)
			if !ok {
				t.Fatalf("frame %d has no numeric seq field: %+v", i, frame)
			}
			if seq <= previous {
				t.Fatalf("frame %d seq = %v, want it greater than %v", i, seq, previous)
			}
			previous = seq
			if _, ok := frame["action"].(string); !ok {
				t.Fatalf("frame %d has no action field: %+v", i, frame)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 2 frames arrived; writeEvent must always emit seq", i)
		}
	}
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func TestAcquireSlotBoundsConcurrency(t *testing.T) {
	ingress := newTestIngress(nil, "bot-1", "balda")

	releases := make([]func(), 0, websocketMaxConcurrentTasks)
	for i := 0; i < websocketMaxConcurrentTasks; i++ {
		release, ok := ingress.acquireSlot()
		if !ok {
			t.Fatalf("acquireSlot() refused slot %d of %d", i+1, websocketMaxConcurrentTasks)
		}
		releases = append(releases, release)
	}

	if _, ok := ingress.acquireSlot(); ok {
		t.Fatal("acquireSlot() granted a slot beyond the concurrency limit")
	}

	// Releasing one slot must make exactly one more available.
	releases[0]()
	if _, ok := ingress.acquireSlot(); !ok {
		t.Fatal("acquireSlot() refused a slot after one was released")
	}
}

func TestPostTimeConvertsMillisecondTimestamp(t *testing.T) {
	post := Post{CreateAt: 1790409344288}
	got := postTime(post)
	if got.IsZero() {
		t.Fatal("postTime() returned the zero time for a provided timestamp")
	}
	if want := time.UnixMilli(1790409344288).UTC(); !got.Equal(want) {
		t.Fatalf("postTime() = %v, want %v", got, want)
	}

	// A missing timestamp must fall back to now rather than the zero time, so
	// downstream session bookkeeping never sees year 1.
	if fallback := postTime(Post{}); fallback.IsZero() {
		t.Fatal("postTime() fell back to the zero time for a missing timestamp")
	}
}

func TestIngressLifecycleIsInertWhenDisabled(t *testing.T) {
	ingress := NewIngress(IngressParams{
		Processor:   &recordingProcessor{},
		Client:      NewClient("http://localhost:8065", "token", "bot-1"),
		Enabled:     false,
		BotUserID:   "bot-1",
		BotUsername: "balda",
		Logger:      zerolog.Nop(),
	})

	// A disabled transport must not start a websocket session, and Start/Stop
	// must stay safe to call in the fx lifecycle.
	if err := ingress.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v, want nil when disabled", err)
	}
	if err := ingress.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v, want nil when disabled", err)
	}
}

func TestProcessInboundErrorDoesNotKillIngress(t *testing.T) {
	processor := &recordingProcessor{inboundErr: errors.New("processing failed")}
	ingress := newTestIngress(processor, "bot-1", "balda")

	// A processor error is logged and swallowed; the ingress keeps running.
	ingress.processPosted(context.Background(), PostedData{ChannelType: channelTypeDirect}, Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "dm-channel-1",
		Message:   "hello",
	})

	messages := processor.inboundMessages()
	if len(messages) != 1 {
		t.Fatalf("ProcessInbound called %d times, want 1", len(messages))
	}
}

func TestNewIngressKeepsBotUserIDVerbatim(t *testing.T) {
	ingress := NewIngress(IngressParams{
		Enabled:     true,
		Client:      NewClient("http://localhost:8065", "token", "bot-from-client"),
		BotUserID:   "explicit-bot",
		Logger:      zerolog.Nop(),
		Commands:    nil,
	})
	if got, want := ingress.botUserID, "explicit-bot"; got != want {
		t.Fatalf("botUserID = %q, want the configured value %q", got, want)
	}
}

func TestStartRefusesWithoutBotUserID(t *testing.T) {
	// Without a bot user id the ingress cannot tell its own posts apart from a
	// user's, so it must refuse to start rather than risk answering itself.
	ingress := NewIngress(IngressParams{
		Enabled:     true,
		Client:      NewClient("http://localhost:8065", "token", "bot-1"),
		BotUserID:   "",
		BotUsername: "balda",
		Logger:      zerolog.Nop(),
	})

	if err := ingress.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want an error when the bot user id is missing")
	}
	if err := ingress.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() after a refused start error = %v, want nil", err)
	}
}

func TestStartRefusesWithoutClient(t *testing.T) {
	ingress := NewIngress(IngressParams{
		Enabled:     true,
		Client:      nil,
		BotUserID:   "bot-1",
		BotUsername: "balda",
		Logger:      zerolog.Nop(),
	})

	if err := ingress.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want an error when the client is missing")
	}
}

func TestLocatorBuiltForAcceptedPostCarriesChannel(t *testing.T) {
	processor := &recordingProcessor{inboundDone: make(chan struct{}, 1)}
	ingress := newTestIngress(processor, "bot-1", "balda")

	frame := postedEventPayload(t, Post{
		ID:        "post-1",
		UserID:    "user-1",
		ChannelID: "channel-9",
		Message:   "@balda hi",
	}, channelTypeOpen, "Alice")

	ingress.handleFrame(context.Background(), frame)

	select {
	case <-processor.inboundDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessInbound was not called")
	}
	messages := processor.inboundMessages()
	if len(messages) != 1 {
		t.Fatalf("ProcessInbound called %d times, want 1", len(messages))
	}
	locator := messages[0].Locator
	if got, want := locator.ChannelType, string(deliverycmd.ChannelTypeMattermost); got != want {
		t.Fatalf("locator.ChannelType = %q, want %q", got, want)
	}
	if got := ChannelIDOf(locator); got != "channel-9" {
		t.Fatalf("locator channel = %q, want %q", got, "channel-9")
	}
}
