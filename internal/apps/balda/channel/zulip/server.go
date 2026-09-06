package zulip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	zulipWebhookMaxBodyBytes       = 1 << 20
	zulipWebhookReadHeaderTimeout  = 5 * time.Second
	zulipWebhookReadTimeout        = 10 * time.Second
	zulipWebhookWriteTimeout       = 10 * time.Second
	zulipWebhookIdleTimeout        = 30 * time.Second
	zulipWebhookProcessingTimeout  = 5 * time.Minute
	zulipWebhookMaxConcurrentTasks = 16
)

// Server handles inbound Zulip webhook HTTP requests.
type Server struct {
	processor    InboundProcessor
	webhookToken string
	listenAddr   string
	webhookPath  string
	enabled      bool
	logger       zerolog.Logger

	server     *http.Server
	ln         net.Listener
	processSem chan struct{}
	processWG  sync.WaitGroup
}

// ZulipBaldaHandler is an alias for Server for backward compatibility.
type ZulipBaldaHandler = Server

type ServerParams struct {
	fx.In

	Processor         InboundProcessor `optional:"true"`
	ZulipWebhookToken string           `name:"balda_zulip_webhook_token"`
	ZulipListenAddr   string           `name:"balda_zulip_listen_addr"`
	ZulipWebhookPath  string           `name:"balda_zulip_webhook_path"`
	ZulipEnabled      bool             `name:"balda_zulip_webhook_enabled"`
	Logger            zerolog.Logger
}

// NewServer creates a Zulip webhook server carrier.
func NewServer(params ServerParams) *Server {
	return &Server{
		processor:    params.Processor,
		webhookToken: strings.TrimSpace(params.ZulipWebhookToken),
		listenAddr:   strings.TrimSpace(params.ZulipListenAddr),
		webhookPath:  strings.TrimSpace(params.ZulipWebhookPath),
		enabled:      params.ZulipEnabled,
		logger:       params.Logger.With().Str("component", "balda.channel.zulip.server").Logger(),
		processSem:   make(chan struct{}, zulipWebhookMaxConcurrentTasks),
	}
}

// NewZulipBaldaHandler creates a ZulipBaldaHandler.
var NewZulipBaldaHandler = NewServer

// Start begins accepting configured Zulip webhook requests.
func (s *Server) Start(ctx context.Context) error { return s.onStart(ctx) }

// Stop gracefully shuts down the Zulip receiver.
func (s *Server) Stop(ctx context.Context) error { return s.onStop(ctx) }

func (s *Server) onStart(_ context.Context) error {
	if !s.enabled {
		s.logger.Info().Msg("zulip webhook disabled; skipping server start")
		return nil
	}
	if s.processSem == nil {
		s.processSem = make(chan struct{}, zulipWebhookMaxConcurrentTasks)
	}

	path, err := normalizeZulipWebhookPath(s.webhookPath)
	if err != nil {
		return err
	}
	listenAddr := s.listenAddr
	if listenAddr == "" {
		listenAddr = ":8090"
	}

	mux := http.NewServeMux()
	mux.HandleFunc(path, s.handleWebhook)
	s.server = &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: zulipWebhookReadHeaderTimeout,
		ReadTimeout:       zulipWebhookReadTimeout,
		WriteTimeout:      zulipWebhookWriteTimeout,
		IdleTimeout:       zulipWebhookIdleTimeout,
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen zulip webhook endpoint on %q: %w", listenAddr, err)
	}
	s.ln = ln

	go func() {
		s.logger.Info().Str("addr", listenAddr).Str("path", path).Msg("zulip webhook server starting")
		if err := s.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error().Err(err).Msg("zulip webhook server error")
		}
	}()

	return nil
}

func normalizeZulipWebhookPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "/zulip/webhook", nil
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("balda.zulip.webhook.path must start with /")
	}
	return trimmed, nil
}

func (s *Server) onStop(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.Warn().Err(err).Msg("zulip webhook server shutdown error")
		return fmt.Errorf("shutdown zulip webhook server: %w", err)
	}
	if err := s.waitForWebhookProcessing(ctx); err != nil {
		s.logger.Warn().Err(err).Msg("zulip webhook processing shutdown wait error")
		return err
	}
	s.ln = nil
	return nil
}

func (s *Server) waitForWebhookProcessing(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.processWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for zulip webhook processing: %w", ctx.Err())
	}
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, zulipWebhookMaxBodyBytes+1))
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to read zulip webhook body")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body) > zulipWebhookMaxBodyBytes {
		s.logger.Warn().Int("bytes", len(body)).Msg("zulip webhook body too large")
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		s.logger.Warn().Err(err).Msg("failed to decode zulip webhook payload")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if s.webhookToken == "" {
		s.logger.Error().Msg("zulip webhook token is not configured")
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	if !VerifyWebhookToken(payload, s.webhookToken) {
		s.logger.Warn().Str("sender", payload.Message.SenderEmail).Msg("zulip webhook token mismatch")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := ValidateWebhookPayload(payload); err != nil {
		s.logger.Warn().Err(err).Msg("invalid zulip webhook payload")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if IsBotEcho(payload) {
		s.logger.Debug().Str("sender", payload.Message.SenderEmail).Msg("ignoring zulip bot echo")
		writeZulipWebhookNoResponse(w)
		return
	}

	release, ok := s.acquireWebhookProcessSlot()
	if !ok {
		s.logger.Warn().Msg("zulip webhook processing queue full")
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}

	defer release()
	settlement, err := s.processWebhookPayload(context.WithoutCancel(r.Context()), payload)
	if err != nil && settlement.Outcome == turncmd.InboundRetry {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	writeZulipWebhookNoResponse(w)
}

func (s *Server) processWebhookPayload(requestCtx context.Context, payload WebhookPayload) (settlement turncmd.InboundSettlement, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error().
				Interface("panic", recovered).
				Int("sender_id", payload.Message.SenderID).
				Str("session_id", LocatorFromWebhookPayload(payload).SessionID).
				Msg("zulip webhook processing panic recovered")
			settlement = turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}
			err = fmt.Errorf("zulip webhook processing panic: %v", recovered)
		}
	}()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), zulipWebhookProcessingTimeout)
	defer cancel()
	return s.processMessage(ctx, payload)
}

func writeZulipWebhookNoResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"response_not_required": true}`))
}

func (s *Server) acquireWebhookProcessSlot() (func(), bool) {
	if s.processSem == nil {
		return func() {}, true
	}
	select {
	case s.processSem <- struct{}{}:
		return func() { <-s.processSem }, true
	default:
		return nil, false
	}
}

func (s *Server) processMessage(ctx context.Context, payload WebhookPayload) (turncmd.InboundSettlement, error) {
	locator := LocatorFromWebhookPayload(payload)
	senderID := payload.Message.SenderID
	text := NormalizeMessageText(payload)
	isDM := payload.Message.Type == messageTypePrivate

	s.logger.Debug().
		Str("trigger", payload.Trigger).
		Str("type", payload.Message.Type).
		Int("sender_id", senderID).
		Msg("processing zulip message")

	if strings.HasPrefix(text, "/") {
		if s.processor != nil {
			fields := strings.Fields(text)
			cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
			args := ""
			if len(fields) > 1 {
				args = strings.Join(fields[1:], " ")
			}
			if cmd == "user" && strings.HasPrefix(args, "invite") {
				args = "add" + strings.TrimPrefix(args, "invite")
			}
			command := InboundCommand{Locator: locator, MessageID: payload.Message.ID, SenderID: senderID, Command: cmd, Args: args, Direct: isDM}
			if !commandSupported(cmd) {
				return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}, s.processor.HandleUnsupportedCommand(ctx, command)
			}
			err := s.processor.HandleCommand(ctx, InboundCommand{
				Locator:   locator,
				MessageID: payload.Message.ID,
				SenderID:  senderID,
				Command:   cmd,
				Args:      args,
				Direct:    isDM,
			})
			return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}, err
		}
		return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}, nil
	}

	if s.processor != nil {
		return s.processor.ProcessInbound(ctx, InboundMessage{
			Locator:     locator,
			MessageID:   payload.Message.ID,
			SenderID:    senderID,
			SenderEmail: payload.Message.SenderEmail,
			Text:        text,
			Direct:      isDM,
			ReceivedAt:  time.Now(),
		})
	}

	return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}, nil
}
