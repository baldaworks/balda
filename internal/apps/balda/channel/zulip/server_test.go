package zulip

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

type mockInboundProcessor struct {
	mu           sync.Mutex
	inboundCalls []InboundMessage
	commandCalls []InboundCommand
	inboundErr   error
	commandErr   error
	settlement   turncmd.InboundSettlement
	blockInbound chan struct{}
}

func (m *mockInboundProcessor) ProcessInbound(_ context.Context, msg InboundMessage) (turncmd.InboundSettlement, error) {
	if m.blockInbound != nil {
		<-m.blockInbound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inboundCalls = append(m.inboundCalls, msg)
	if m.settlement.Outcome == "" {
		return turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}, m.inboundErr
	}
	return m.settlement, m.inboundErr
}

func (m *mockInboundProcessor) HandleCommand(_ context.Context, cmd InboundCommand) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commandCalls = append(m.commandCalls, cmd)
	return m.commandErr
}

func TestZulipBaldaHandlerRejectsInvalidWebhookToken(t *testing.T) {
	handler := &ZulipBaldaHandler{
		webhookToken: "expected-token",
		logger:       zerolog.Nop(),
	}
	req := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(`{
		"token":"wrong-token",
		"message":{"sender_email":"user@example.com"}
	}`))
	rec := httptest.NewRecorder()

	handler.handleWebhook(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestZulipBaldaHandlerRejectsMissingWebhookTokenConfiguration(t *testing.T) {
	handler := &ZulipBaldaHandler{
		logger: zerolog.Nop(),
	}
	req := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(`{
		"token":"provided-token",
		"message":{"sender_email":"user@example.com"}
	}`))
	rec := httptest.NewRecorder()

	handler.handleWebhook(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestZulipBaldaHandlerRejectsUnsupportedWebhookMethod(t *testing.T) {
	handler := &ZulipBaldaHandler{
		webhookToken: "expected-token",
		logger:       zerolog.Nop(),
	}
	req := httptest.NewRequest(http.MethodGet, "/zulip/webhook", nil)
	rec := httptest.NewRecorder()

	handler.handleWebhook(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", got, http.MethodPost)
	}
}

func TestZulipBaldaHandlerOnStartFailsWhenListenAddressInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().String()
	handler := &ZulipBaldaHandler{
		listenAddr:   addr,
		webhookPath:  "/zulip/webhook",
		webhookToken: "token",
		enabled:      true,
		logger:       zerolog.Nop(),
	}

	startErr := handler.Start(context.Background())
	if startErr == nil {
		t.Fatal("Start() error = nil, want bind failure")
	}
	if !strings.Contains(startErr.Error(), addr) {
		t.Fatalf("Start() error = %v, want address %q mentioned", startErr, addr)
	}
}

func TestZulipBaldaHandlerOnStartConfiguresHTTPTimeouts(t *testing.T) {
	handler := &ZulipBaldaHandler{
		listenAddr:   "127.0.0.1:0",
		webhookPath:  "/zulip/webhook",
		webhookToken: "token",
		enabled:      true,
		logger:       zerolog.Nop(),
	}

	if err := handler.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() { _ = handler.Stop(context.Background()) }()

	if handler.server == nil {
		t.Fatal("server = nil, want active http.Server")
	}
	if handler.server.ReadHeaderTimeout != zulipWebhookReadHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", handler.server.ReadHeaderTimeout, zulipWebhookReadHeaderTimeout)
	}
	if handler.server.ReadTimeout != zulipWebhookReadTimeout {
		t.Fatalf("ReadTimeout = %v, want %v", handler.server.ReadTimeout, zulipWebhookReadTimeout)
	}
	if handler.server.WriteTimeout != zulipWebhookWriteTimeout {
		t.Fatalf("WriteTimeout = %v, want %v", handler.server.WriteTimeout, zulipWebhookWriteTimeout)
	}
	if handler.server.IdleTimeout != zulipWebhookIdleTimeout {
		t.Fatalf("IdleTimeout = %v, want %v", handler.server.IdleTimeout, zulipWebhookIdleTimeout)
	}
}

func TestZulipBaldaHandlerOnStartRejectsInvalidWebhookPath(t *testing.T) {
	handler := &ZulipBaldaHandler{
		listenAddr:   "127.0.0.1:0",
		webhookPath:  "invalid-path-without-slash",
		webhookToken: "token",
		enabled:      true,
		logger:       zerolog.Nop(),
	}

	err := handler.Start(context.Background())
	if err == nil {
		t.Fatal("Start() error = nil, want invalid webhook path error")
	}
	if !strings.Contains(err.Error(), "balda.zulip.webhook.path") {
		t.Fatalf("Start() error = %v, want path validation message", err)
	}
}

func TestZulipBaldaHandlerOnStopReturnsShutdownError(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{})
	server := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(entered)
			<-block
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() {
		close(block)
		_ = server.Close()
		_ = ln.Close()
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String(), nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered

	handler := &ZulipBaldaHandler{
		server: server,
		logger: zerolog.Nop(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = handler.onStop(ctx)
	if err == nil {
		t.Fatal("Stop() error = nil, want context error")
	}
	if !strings.Contains(err.Error(), "shutdown zulip webhook server") {
		t.Fatalf("Stop() error = %v, want shutdown context error", err)
	}
}

func TestZulipBaldaHandlerOnStopWaitsForWebhookProcessing(t *testing.T) {
	handler := &ZulipBaldaHandler{
		listenAddr:   "127.0.0.1:0",
		webhookPath:  "/zulip/webhook",
		webhookToken: "token",
		enabled:      true,
		logger:       zerolog.Nop(),
	}

	if err := handler.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	unblock := make(chan struct{})
	done := make(chan struct{})
	handler.processWG.Add(1)
	go func() {
		defer handler.processWG.Done()
		<-unblock
		close(done)
	}()

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- handler.Stop(context.Background())
	}()

	select {
	case <-done:
		t.Fatal("processing completed before unblock signal")
	case <-time.After(50 * time.Millisecond):
	}

	close(unblock)
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out waiting for webhook processing")
	}
}

func TestZulipBaldaHandlerOnStopReturnsProcessingWaitError(t *testing.T) {
	handler := &ZulipBaldaHandler{
		listenAddr:   "127.0.0.1:0",
		webhookPath:  "/zulip/webhook",
		webhookToken: "token",
		enabled:      true,
		logger:       zerolog.Nop(),
	}

	if err := handler.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	handler.processWG.Add(1)
	go func() {
		defer handler.processWG.Done()
		<-block
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := handler.Stop(ctx)
	if err == nil {
		t.Fatal("Stop() error = nil, want wait timeout error")
	}
	if !strings.Contains(err.Error(), "wait for zulip webhook processing") {
		t.Fatalf("Stop() error = %v, want wait error", err)
	}
}

func TestZulipBaldaHandlerRejectsOversizedWebhookBody(t *testing.T) {
	handler := &ZulipBaldaHandler{
		webhookToken: "expected-token",
		logger:       zerolog.Nop(),
	}
	oversizedBody := strings.Repeat("a", zulipWebhookMaxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(oversizedBody))
	rec := httptest.NewRecorder()

	handler.handleWebhook(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestZulipBaldaHandlerReturnsBusyWhenProcessingSlotsFull(t *testing.T) {
	mockProc := &mockInboundProcessor{
		blockInbound: make(chan struct{}),
	}
	t.Cleanup(func() { close(mockProc.blockInbound) })

	handler := &ZulipBaldaHandler{
		processor:    mockProc,
		webhookToken: "expected-token",
		processSem:   make(chan struct{}, 1),
		logger:       zerolog.Nop(),
	}

	body := `{
		"token":"expected-token",
		"message":{
			"sender_id":101,
			"sender_email":"user@example.com",
			"type":"stream",
			"stream_id":42,
			"subject":"ops",
			"content":"hello"
		}
	}`

	req1 := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(body))
	rec1 := httptest.NewRecorder()
	started := make(chan struct{})
	go func() {
		close(started)
		handler.handleWebhook(rec1, req1)
	}()

	<-started
	time.Sleep(50 * time.Millisecond)

	req2 := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(body))
	rec2 := httptest.NewRecorder()
	handler.handleWebhook(rec2, req2)

	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d for full processing queue", rec2.Code, http.StatusServiceUnavailable)
	}
}

func TestZulipBaldaHandlerIgnoresBotEchoBeforeProcessingQueue(t *testing.T) {
	mockProc := &mockInboundProcessor{}
	handler := &ZulipBaldaHandler{
		processor:    mockProc,
		webhookToken: "expected-token",
		processSem:   make(chan struct{}, 1),
		logger:       zerolog.Nop(),
	}

	body := `{
		"token":"expected-token",
		"bot_email":"bot@example.com",
		"message":{
			"sender_id":999,
			"sender_email":"bot@example.com",
			"type":"stream",
			"stream_id":42,
			"subject":"ops",
			"content":"bot echo"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.handleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(mockProc.inboundCalls) != 0 {
		t.Fatalf("inbound calls = %d, want 0 for bot echo", len(mockProc.inboundCalls))
	}
	if got := len(handler.processSem); got != 0 {
		t.Fatalf("process slot count = %d, want 0", got)
	}
}

type panickingProcessor struct{}

func (panickingProcessor) ProcessInbound(context.Context, InboundMessage) (turncmd.InboundSettlement, error) {
	panic("unexpected processor failure")
}

func (panickingProcessor) HandleCommand(context.Context, InboundCommand) error {
	panic("unexpected processor command failure")
}

func TestZulipBaldaHandlerRecoversProcessingPanicAndReleasesSlot(t *testing.T) {
	handler := &ZulipBaldaHandler{
		processor:    panickingProcessor{},
		webhookToken: "expected-token",
		processSem:   make(chan struct{}, 1),
		logger:       zerolog.Nop(),
	}

	body := `{
		"token":"expected-token",
		"message":{
			"sender_id":101,
			"sender_email":"user@example.com",
			"type":"stream",
			"stream_id":42,
			"subject":"ops",
			"content":"hello"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.handleWebhook(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := len(handler.processSem); got != 0 {
		t.Fatalf("process slot count = %d, want 0 after recovered panic", got)
	}
}

func TestZulipBaldaHandlerRejectsInvalidAuthenticatedPayload(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing sender id",
			body: `{"token":"expected-token","message":{"sender_id":0,"sender_email":"user@example.com","type":"stream","stream_id":42}}`,
		},
		{
			name: "missing sender email",
			body: `{"token":"expected-token","message":{"sender_id":101,"sender_email":" ","type":"stream","stream_id":42}}`,
		},
		{
			name: "missing stream id for stream message",
			body: `{"token":"expected-token","message":{"sender_id":101,"sender_email":"user@example.com","type":"stream","stream_id":0}}`,
		},
		{
			name: "unsupported message type",
			body: `{"token":"expected-token","message":{"sender_id":101,"sender_email":"user@example.com","type":"channel"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &ZulipBaldaHandler{
				webhookToken: "expected-token",
				processSem:   make(chan struct{}, 1),
				logger:       zerolog.Nop(),
			}
			req := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			handler.handleWebhook(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if got := len(handler.processSem); got != 0 {
				t.Fatalf("process slot count = %d, want 0 for rejected payload", got)
			}
		})
	}
}

func TestValidateZulipWebhookPayloadAllowsEmptyStreamSubject(t *testing.T) {
	err := ValidateWebhookPayload(WebhookPayload{
		Message: WebhookMessage{
			SenderID:    101,
			SenderEmail: "user@example.com",
			Type:        "stream",
			StreamID:    42,
		},
	})
	if err != nil {
		t.Fatalf("ValidateWebhookPayload() error = %v, want nil for empty Zulip topic", err)
	}
}

func TestZulipServerForwardsCommandToProcessor(t *testing.T) {
	mockProc := &mockInboundProcessor{}
	server := NewServer(ServerParams{
		Processor:         mockProc,
		ZulipWebhookToken: "token",
		Logger:            zerolog.Nop(),
	})

	body := `{
		"token":"token",
		"message":{
			"sender_id":101,
			"sender_email":"user@example.com",
			"type":"private",
			"content":"/reset"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()

	server.handleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(mockProc.commandCalls) != 1 {
		t.Fatalf("command calls = %d, want 1", len(mockProc.commandCalls))
	}
	cmd := mockProc.commandCalls[0]
	if cmd.Command != "reset" {
		t.Fatalf("cmd.Command = %q, want reset", cmd.Command)
	}
	if cmd.SenderID != 101 {
		t.Fatalf("cmd.SenderID = %d, want 101", cmd.SenderID)
	}
	if !cmd.Direct {
		t.Fatal("cmd.Direct = false, want true for private chat")
	}
}

func TestZulipServerForwardsMessageToProcessor(t *testing.T) {
	mockProc := &mockInboundProcessor{}
	server := NewServer(ServerParams{
		Processor:         mockProc,
		ZulipWebhookToken: "token",
		Logger:            zerolog.Nop(),
	})

	body := `{
		"token":"token",
		"message":{
			"id":42,
			"sender_id":101,
			"sender_email":"user@example.com",
			"type":"stream",
			"stream_id":7,
			"subject":"general",
			"content":"hello world"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/zulip/webhook", strings.NewReader(body))
	rec := httptest.NewRecorder()

	server.handleWebhook(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(mockProc.inboundCalls) != 1 {
		t.Fatalf("inbound calls = %d, want 1", len(mockProc.inboundCalls))
	}
	msg := mockProc.inboundCalls[0]
	if msg.Text != "hello world" {
		t.Fatalf("msg.Text = %q, want 'hello world'", msg.Text)
	}
	if msg.MessageID != 42 {
		t.Fatalf("msg.MessageID = %d, want 42", msg.MessageID)
	}
	if msg.SenderID != 101 {
		t.Fatalf("msg.SenderID = %d, want 101", msg.SenderID)
	}
}
