package slackagent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

const (
	webhookMaxBodyBytes       = 1 << 20
	webhookReadHeaderTimeout  = 5 * time.Second
	webhookReadTimeout        = 10 * time.Second
	webhookWriteTimeout       = 10 * time.Second
	webhookIdleTimeout        = 30 * time.Second
	webhookProcessingTimeout  = 5 * time.Minute
	webhookMaxConcurrentTasks = 16
	signatureVersion          = "v0"
)

type Config struct {
	Enabled       bool
	ListenAddr    string
	EventsPath    string
	SigningSecret string
}

type Server struct {
	inboundProcessor InboundProcessor
	turnCanceller    TurnCanceller
	config           Config
	logger           zerolog.Logger

	server     *http.Server
	ln         net.Listener
	processSem chan struct{}
	processWG  sync.WaitGroup
}

type InboundProcessor interface {
	ProcessInbound(ctx context.Context, envelope IngressEnvelope) (turncmd.InboundSettlement, error)
}

type TurnCanceller interface {
	CancelTurn(ctx context.Context, stopped SessionStopped) error
}

func NewServer(processor InboundProcessor, canceller TurnCanceller, config Config, logger zerolog.Logger) *Server {
	return &Server{
		inboundProcessor: processor,
		turnCanceller:    canceller,
		config:           config,
		logger:           logger.With().Str("component", "balda.channel.slackagent").Logger(),
		processSem:       make(chan struct{}, webhookMaxConcurrentTasks),
	}
}

func (h *Server) Start(ctx context.Context) error { return h.onStart(ctx) }
func (h *Server) Stop(ctx context.Context) error  { return h.onStop(ctx) }

func (h *Server) onStart(context.Context) error {
	if !h.config.Enabled {
		h.logger.Debug().Msg("slackagent disabled; skipping server start")
		return nil
	}
	eventsPath, err := normalizePath(h.config.EventsPath, "/slack/agent/events")
	if err != nil {
		return err
	}
	listenAddr := strings.TrimSpace(h.config.ListenAddr)
	if listenAddr == "" {
		listenAddr = ":8092"
	}
	mux := http.NewServeMux()
	mux.HandleFunc(eventsPath, h.handleEvents)
	h.server = &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: webhookReadHeaderTimeout,
		ReadTimeout:       webhookReadTimeout,
		WriteTimeout:      webhookWriteTimeout,
		IdleTimeout:       webhookIdleTimeout,
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen slackagent endpoint on %q: %w", listenAddr, err)
	}
	h.ln = ln
	go func() {
		h.logger.Info().Str("addr", listenAddr).Str("events_path", eventsPath).Msg("slackagent http server starting")
		if err := h.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			h.logger.Error().Err(err).Msg("slackagent http server error")
		}
	}()
	return nil
}

func (h *Server) onStop(ctx context.Context) error {
	if h.server == nil {
		return nil
	}
	if err := h.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown slackagent server: %w", err)
	}
	done := make(chan struct{})
	go func() {
		h.processWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for slackagent processing: %w", ctx.Err())
	}
}

func (h *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	body, ok := h.readAndVerifyRequest(w, r)
	if !ok {
		return
	}
	env, err := DecodeIngressEnvelope(body, time.Now())
	if err != nil {
		h.logger.Warn().Err(err).Msg("failed to decode slackagent event payload")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if env.Type == "url_verification" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(env.Challenge))
		return
	}
	release, ok := h.acquireProcessSlot()
	if !ok {
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return
	}
	defer release()
	settlement, err := h.processEvent(context.WithoutCancel(r.Context()), env)
	if err != nil && settlement.Outcome == turncmd.InboundRetry {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Server) readAndVerifyRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBodyBytes+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > webhookMaxBodyBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if err := verifySignature(strings.TrimSpace(h.config.SigningSecret), r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), body, time.Now()); err != nil {
		h.logger.Warn().Err(err).Msg("slackagent signature verification failed")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}

func (h *Server) acquireProcessSlot() (func(), bool) {
	if h.processSem == nil {
		return func() {}, true
	}
	select {
	case h.processSem <- struct{}{}:
		return func() { <-h.processSem }, true
	default:
		return nil, false
	}
}

func (h *Server) processEvent(requestCtx context.Context, env IngressEnvelope) (turncmd.InboundSettlement, error) {
	ctx, cancel := context.WithTimeout(requestCtx, webhookProcessingTimeout)
	defer cancel()
	if env.IgnoreEvent {
		return terminalInbound(), nil
	}
	if env.Stopped != nil {
		if h.turnCanceller == nil {
			return retryInbound(), fmt.Errorf("slackagent turn canceller is required")
		}
		if err := h.turnCanceller.CancelTurn(ctx, *env.Stopped); err != nil {
			h.logger.Warn().Err(err).Str("address_key", env.Stopped.Locator.AddressKey).Msg("failed to cancel slackagent turn")
			return retryInbound(), err
		}
		return terminalInbound(), nil
	}
	if h.inboundProcessor == nil {
		return retryInbound(), fmt.Errorf("slackagent inbound processor is required")
	}
	settlement, err := h.inboundProcessor.ProcessInbound(ctx, env)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", env.Locator.SessionID).Msg("failed to process slackagent inbound event")
	}
	return settlement, err
}

func normalizePath(path string, fallback string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fallback, nil
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("slack path %q must start with /", path)
	}
	return trimmed, nil
}

func verifySignature(secret, timestamp, signature string, body []byte, now time.Time) error {
	trimmedSecret := strings.TrimSpace(secret)
	if trimmedSecret == "" {
		return fmt.Errorf("slack signing secret is required")
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid slack request timestamp")
	}
	requestTime := time.Unix(ts, 0)
	if now.Sub(requestTime) > 5*time.Minute || requestTime.Sub(now) > 5*time.Minute {
		return fmt.Errorf("stale slack request timestamp")
	}
	base := signatureVersion + ":" + strings.TrimSpace(timestamp) + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(trimmedSecret))
	_, _ = mac.Write([]byte(base))
	expected := signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(strings.TrimSpace(signature))) {
		return fmt.Errorf("slack request signature mismatch")
	}
	return nil
}

func terminalInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundTerminal}
}

func retryInbound() turncmd.InboundSettlement {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}
}
